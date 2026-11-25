package main

// 番号付き SQL のマイグレーション実行器（必要な機能だけの小さなもの）。backend/app/migrate.py の移植。
//
//	api migrate --status     適用状況の表示だけ（DB を変えない）
//	api migrate --dry-run    適用するものの一覧と事前の検査だけ（DB を変えない）
//	api migrate              未適用のものを順に適用する
//
//   - 0001 は、db/schema.sql と db/seed_codes.sql（Postgres の初回起動で流れる初期スキーマ）を指す。実行器はこれを流さない。
//     DB に初期スキーマがあるときだけ、表・列・制約・索引・件数を baseline_fingerprint.json と照合し、一致したときに 0001 を「適用済み」と記録する。
//     1 つでも違えば、記録せずに止まる（想定外の DB に、先のマイグレーションを重ねないため）
//   - 0002 以降は migrations/NNNN_名前.sql（バイナリに埋め込む）。1 ファイルを 1 トランザクションで流し、成功したときだけ記録する（lock_timeout 5 秒）
//   - 記録にはファイルの sha256 を残し、適用済みのファイルが書き換わっていたら止まる。直すときは新しい番号のファイルを足す
//   - 同時に 2 つ動かないよう、アドバイザリーロックを取る。DROP / TRUNCATE を含むファイルは、このリポジトリでは足さない

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations
var migrationsFS embed.FS

const (
	baselineVersion  = "0001"
	migrationLockKey = 7_204_001
	fingerprintPath  = "migrations/baseline_fingerprint.json"
)

var (
	migrationFile = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
	masterTables  = []string{"areas", "stations", "occupations", "employments"}
)

// migrationError は続行してはいけない状態（スキーマの不一致、チェックサムの不一致など）。メッセージは固定の説明だけで、接続情報を含めない。
type migrationError struct{ msg string }

func (e *migrationError) Error() string { return e.msg }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type migration struct {
	version, name string
	sql           []byte
}

// migrationFiles は、0002 以降を番号順に。番号の重複・欠番は migrationError。
func migrationFiles() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var found []migration
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, &migrationError{"マイグレーションのファイル名が規則に合いません: " + e.Name()}
		}
		data, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		found = append(found, migration{version: m[1], name: strings.TrimSuffix(e.Name(), ".sql"), sql: data})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	seen := map[string]bool{}
	for _, f := range found {
		if seen[f.version] {
			return nil, &migrationError{"同じ番号のマイグレーションが複数あります"}
		}
		seen[f.version] = true
	}
	for i, f := range found {
		if want := fmt.Sprintf("%04d", i+2); f.version != want {
			versions := make([]string, len(found))
			for k, x := range found {
				versions[k] = x.version
			}
			return nil, &migrationError{fmt.Sprintf("番号が 0002 から連続していません: %v", versions)}
		}
	}
	return found, nil
}

func noneIfNil(s *string) string {
	if s == nil {
		return "None"
	}
	return *s
}

// fingerprint は public スキーマの形（表・列・制約・索引・ビュー名・マスタの件数）を、並べ替え済みの文字列の一覧にする。schema_migrations は含めない。
func fingerprint(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	var lines []string
	rows, err := conn.Query(ctx, "SELECT table_name, column_name, data_type, is_nullable, column_default, is_identity FROM information_schema.columns "+
		"WHERE table_schema = 'public' AND table_name <> 'schema_migrations'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t, c, dt, nullable, identity string
		var def *string
		if err := rows.Scan(&t, &c, &dt, &nullable, &def, &identity); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("column %s.%s %s null=%s default=%s identity=%s", t, c, dt, nullable, noneIfNil(def), identity))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = conn.Query(ctx, "SELECT conrelid::regclass::text, conname, pg_get_constraintdef(oid) FROM pg_constraint "+
		"WHERE connamespace = 'public'::regnamespace AND conrelid::regclass::text <> 'schema_migrations'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("constraint %s %s %s", a, b, c))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = conn.Query(ctx, "SELECT tablename, indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename <> 'schema_migrations'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a, b, c string
		if err := rows.Scan(&a, &b, &c); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("index %s %s %s", a, b, c))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = conn.Query(ctx, "SELECT viewname FROM pg_views WHERE schemaname = 'public'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, "view "+v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, table := range masterTables {
		var n int64
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil { // 固定の表名
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("rows %s %d", table, n))
	}
	sort.Strings(lines)
	return lines, nil
}

func baselineDiff(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	raw, err := migrationsFS.ReadFile(fingerprintPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for _, l := range doc.Lines {
		expected[l] = true
	}
	actualLines, err := fingerprint(ctx, conn)
	if err != nil {
		return nil, err
	}
	actual := map[string]bool{}
	for _, l := range actualLines {
		actual[l] = true
	}
	var missing, extra []string
	for l := range expected {
		if !actual[l] {
			missing = append(missing, "- 足りない: "+l)
		}
	}
	for l := range actual {
		if !expected[l] {
			extra = append(extra, "- 想定外: "+l)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return append(missing, extra...), nil
}

type appliedRow struct{ name, checksum string }

func appliedMigrations(ctx context.Context, conn *pgx.Conn) (map[string]appliedRow, error) {
	out := map[string]appliedRow{}
	var absent bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NULL").Scan(&absent); err != nil {
		return nil, err
	}
	if absent {
		return out, nil
	}
	rows, err := conn.Query(ctx, "SELECT version, name, checksum FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		var r appliedRow
		if err := rows.Scan(&v, &r.name, &r.checksum); err != nil {
			return nil, err
		}
		out[v] = r
	}
	return out, rows.Err()
}

type migrationPlan struct {
	baseline string // recorded / verified / missing / mismatch
	pending  []migration
	problems []string
	applied  []string
}

// planMigrations は DB を変えずに、今の状態を調べる。
func planMigrations(ctx context.Context, conn *pgx.Conn) (migrationPlan, error) {
	var p migrationPlan
	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return p, err
	}
	files, err := migrationFiles()
	if err != nil {
		return p, err
	}
	p.baseline = "recorded"
	if _, ok := applied[baselineVersion]; !ok {
		var noRecruits bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass('public.recruits') IS NULL").Scan(&noRecruits); err != nil {
			return p, err
		}
		if noRecruits {
			p.baseline = "missing"
			p.problems = append(p.problems, "初期スキーマがありません。Postgres の初回起動（db/schema.sql）か、psql で db/schema.sql と db/seed_codes.sql を先に流してください。")
		} else {
			diff, err := baselineDiff(ctx, conn)
			if err != nil {
				return p, err
			}
			if len(diff) > 0 {
				p.baseline = "mismatch"
				p.problems = append(p.problems, "DB の形が、想定する初期スキーマと一致しません。適用済みとは記録せず、止めます:")
				p.problems = append(p.problems, diff[:min(30, len(diff))]...)
				if len(diff) > 30 {
					p.problems = append(p.problems, fmt.Sprintf("（ほか %d 件）", len(diff)-30))
				}
			} else {
				p.baseline = "verified"
			}
		}
	}
	known := map[string]bool{baselineVersion: true}
	for _, f := range files {
		known[f.version] = true
		if a, ok := applied[f.version]; ok {
			if a.checksum != sha256Hex(f.sql) {
				p.problems = append(p.problems, fmt.Sprintf("適用済みのマイグレーション %s が書き換えられています（記録したチェックサムと違います）。", f.name))
			}
		} else {
			p.pending = append(p.pending, f)
		}
	}
	for v := range applied {
		p.applied = append(p.applied, v)
		if !known[v] {
			p.problems = append(p.problems, fmt.Sprintf("記録にあるマイグレーション %s のファイルがありません。", v))
		}
	}
	sort.Strings(p.applied)
	return p, nil
}

func pendingNames(p migrationPlan) string {
	if len(p.pending) == 0 {
		return "なし"
	}
	names := make([]string, len(p.pending))
	for i, m := range p.pending {
		names[i] = m.name
	}
	return strings.Join(names, ", ")
}

// applyMigrations は終了コード（0 = 成功、1 = 止まった）。dryRun では DB を変えない。
func applyMigrations(ctx context.Context, url string, dryRun bool, out io.Writer) (int, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return 1, err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(migrationLockKey)); err != nil {
		return 1, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", int64(migrationLockKey))
	}()
	state, err := planMigrations(ctx, conn)
	if err != nil {
		return 1, err
	}
	for _, line := range state.problems {
		fmt.Fprintln(out, line)
	}
	if len(state.problems) > 0 {
		return 1, nil
	}
	if state.baseline == "recorded" {
		fmt.Fprintln(out, "初期スキーマ: 記録済み")
	} else {
		fmt.Fprintln(out, "初期スキーマ: 照合に一致（未記録）")
	}
	fmt.Fprintln(out, "未適用: "+pendingNames(state))
	if dryRun {
		return 0, nil
	}
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, name text NOT NULL, "+
		"checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return 1, err
	}
	if state.baseline == "verified" {
		raw, err := migrationsFS.ReadFile(fingerprintPath)
		if err != nil {
			return 1, err
		}
		if _, err := conn.Exec(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, 'baseline', $2)", baselineVersion, sha256Hex(raw)); err != nil {
			return 1, err
		}
		fmt.Fprintln(out, "0001 baseline を適用済みとして記録しました")
	}
	for _, m := range state.pending {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return 1, err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
			_ = tx.Rollback(ctx)
			return 1, err
		}
		// 1 ファイルに複数の文があるので、simple プロトコルで流す（同じトランザクションの中）
		if _, err := conn.PgConn().Exec(ctx, string(m.sql)).ReadAll(); err != nil {
			_ = tx.Rollback(ctx)
			return 1, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)", m.version, m.name, sha256Hex(m.sql)); err != nil {
			_ = tx.Rollback(ctx)
			return 1, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 1, err
		}
		fmt.Fprintln(out, "適用: "+m.name)
	}
	return 0, nil
}

func migrateMain(args []string, e env) int {
	url := e.str("DATABASE_URL")
	if url == "" {
		fmt.Println("失敗しました（RuntimeError）。DATABASE_URL が未設定です")
		return 1
	}
	ctx := context.Background()
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	report := func(err error) int {
		var me *migrationError
		if errors.As(err, &me) {
			fmt.Printf("失敗しました（MigrationError）。%s\n", me.msg)
		} else {
			// 接続情報を含みうる例外の本文は出さず、種類だけ出す
			fmt.Printf("失敗しました（%T）。DB への接続か SQL の実行でエラーです\n", err)
		}
		return 1
	}
	if has("--write-baseline-fingerprint") { // 保守用: db/schema.sql と db/seed_codes.sql だけを流した作りたての DB から、照合用の形を書き出す
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			return report(err)
		}
		defer conn.Close(ctx)
		lines, err := fingerprint(ctx, conn)
		if err != nil {
			return report(err)
		}
		raw, _ := json.MarshalIndent(map[string]any{"version": 1, "lines": lines}, "", " ")
		path := "migrations/baseline_fingerprint.json"
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			return report(err)
		}
		fmt.Printf("書き出しました: %s\n", path)
		return 0
	}
	if has("--status") {
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			return report(err)
		}
		defer conn.Close(ctx)
		state, err := planMigrations(ctx, conn)
		if err != nil {
			return report(err)
		}
		fmt.Println("適用済み:", orNone(strings.Join(state.applied, ", ")))
		fmt.Println("初期スキーマ:", state.baseline)
		fmt.Println("未適用:", pendingNames(state))
		for _, line := range state.problems {
			fmt.Println(line)
		}
		if len(state.problems) > 0 {
			return 1
		}
		return 0
	}
	code, err := applyMigrations(ctx, url, has("--dry-run"), os.Stdout)
	if err != nil {
		return report(err)
	}
	return code
}

func orNone(s string) string {
	if s == "" {
		return "なし"
	}
	return s
}
