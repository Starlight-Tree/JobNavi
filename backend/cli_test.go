package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func queryRows(t *testing.T, url, sql string, args ...any) [][]string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("SQL: %v\n%s", err, sql)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			row[i] = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(sprint(v), "<nil>", ""), "\n", " "))
		}
		out = append(out, row)
	}
	return out
}

func sprint(v any) string { return fmt.Sprint(v) }

func TestMigrateFreshDatabase(t *testing.T) {
	url := newEmptyDB(t)
	ctx := context.Background()
	var out strings.Builder
	// 事前の検査だけ（DB を変えない）
	if code, err := applyMigrations(ctx, url, true, &out); err != nil || code != 0 {
		t.Fatalf("dry-run: %d %v\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "初期スキーマ: 照合に一致（未記録）") || !strings.Contains(out.String(), "未適用: 0002_images, 0003_google_login") {
		t.Errorf("dry-run の出力:\n%s", out.String())
	}
	if rows := queryRows(t, url, "SELECT to_regclass('public.schema_migrations')::text"); rows[0][0] != "" {
		t.Error("dry-run で schema_migrations を作りました")
	}
	out.Reset()
	if code, err := applyMigrations(ctx, url, false, &out); err != nil || code != 0 {
		t.Fatalf("apply: %d %v\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "0001 baseline を適用済みとして記録しました") || !strings.Contains(out.String(), "適用: 0009_search_fold") {
		t.Errorf("apply の出力:\n%s", out.String())
	}
	files, _ := migrationFiles()
	if rows := queryRows(t, url, "SELECT version FROM schema_migrations ORDER BY version"); len(rows) != len(files)+1 || rows[0][0] != "0001" {
		t.Errorf("記録: %v", rows)
	}
	for _, f := range files {
		if rows := queryRows(t, url, "SELECT checksum FROM schema_migrations WHERE version = $1", f.version); rows[0][0] != sha256Hex(f.sql) {
			t.Errorf("%s のチェックサム", f.name)
		}
	}
	// 2 回目は何もしない
	out.Reset()
	if code, err := applyMigrations(ctx, url, false, &out); err != nil || code != 0 || !strings.Contains(out.String(), "未適用: なし") || !strings.Contains(out.String(), "初期スキーマ: 記録済み") {
		t.Errorf("2 回目: %d %v\n%s", code, err, out.String())
	}
	// 適用済みのファイルが書き換わったら止まる
	conn, _ := pgx.Connect(ctx, url)
	_, _ = conn.Exec(ctx, "UPDATE schema_migrations SET checksum = 'tampered' WHERE version = '0003'")
	conn.Close(ctx)
	out.Reset()
	if code, _ := applyMigrations(ctx, url, false, &out); code != 1 || !strings.Contains(out.String(), "適用済みのマイグレーション 0003_google_login が書き換えられています") {
		t.Errorf("チェックサムの不一致: %d\n%s", code, out.String())
	}
	conn, _ = pgx.Connect(ctx, url)
	_, _ = conn.Exec(ctx, "UPDATE schema_migrations SET checksum = $1 WHERE version = '0003'", sha256Hex(files[1].sql))
	_, _ = conn.Exec(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES ('9999', 'ghost', 'x')")
	conn.Close(ctx)
	out.Reset()
	if code, _ := applyMigrations(ctx, url, false, &out); code != 1 || !strings.Contains(out.String(), "記録にあるマイグレーション 9999 のファイルがありません") {
		t.Errorf("記録だけあるファイル: %d\n%s", code, out.String())
	}
}

func TestMigrateStopsOnUnexpectedSchema(t *testing.T) {
	ctx := context.Background()
	var out strings.Builder
	url := newEmptyDB(t)
	conn, _ := pgx.Connect(ctx, url)
	_, err := conn.Exec(ctx, "ALTER TABLE areas ADD COLUMN extra text")
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code, _ := applyMigrations(ctx, url, false, &out); code != 1 || !strings.Contains(out.String(), "DB の形が、想定する初期スキーマと一致しません") || !strings.Contains(out.String(), "想定外: column areas.extra") {
		t.Errorf("想定外の列: %d\n%s", code, out.String())
	}
	if rows := queryRows(t, url, "SELECT to_regclass('public.schema_migrations')::text"); rows[0][0] != "" {
		t.Error("止まったのに、記録を作りました")
	}
	// 初期スキーマが無い（空の DB）
	name := "jobnavi_go_nobase_" + itoa(time.Now().UnixNano()%1_000_000)
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	admin.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := pgx.Connect(context.Background(), adminURL); err == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(context.Background())
		}
	})
	out.Reset()
	if code, _ := applyMigrations(ctx, withDB(adminURL, name), false, &out); code != 1 || !strings.Contains(out.String(), "初期スキーマがありません") {
		t.Errorf("空の DB: %d\n%s", code, out.String())
	}
}

func TestMigrationFilesAreConsecutive(t *testing.T) {
	files, err := migrationFiles()
	if err != nil || len(files) < 8 {
		t.Fatalf("%v %d", err, len(files))
	}
	for i, f := range files {
		if f.version != "00"+itoa(int64(i+2)/10)+itoa(int64(i+2)%10) {
			t.Errorf("番号 %s（%d 番目）", f.version, i)
		}
		upper := strings.ToUpper(string(f.sql))
		if strings.Contains(upper, "DROP TABLE") || strings.Contains(upper, "TRUNCATE") {
			t.Errorf("%s に DROP TABLE / TRUNCATE があります（足さない決まり）", f.name)
		}
	}
}

func TestSeedRefusesWithoutDemoDataFlag(t *testing.T) {
	url := newEmptyDB(t)
	out := captureStdout(t, func() {
		if code := seedMain(nil, testEnvMap(map[string]string{"DATABASE_URL": url})); code != 1 {
			t.Errorf("DEMO_DATA が無いのに実行しました: %d", code)
		}
	})
	if !strings.Contains(out, "DEMO_DATA=allow を渡した開発・テストの環境だけです") {
		t.Errorf("出力: %s", out)
	}
	if code := seedImagesMain([]string{"--source-dir", "x"}, testEnvMap(map[string]string{"DATABASE_URL": url})); code != 1 {
		t.Error("seed-images も、DEMO_DATA が無いと実行しない")
	}
}

func TestSeedIsIdempotentAndResets(t *testing.T) {
	url := newEmptyDB(t)
	ctx := context.Background()
	var sink strings.Builder
	if code, err := applyMigrations(ctx, url, false, &sink); err != nil || code != 0 {
		t.Fatal(err)
	}
	e := testEnvMap(map[string]string{"DATABASE_URL": url, "DEMO_DATA": "allow"})
	run := func(args ...string) string {
		var code int
		out := captureStdout(t, func() { code = seedMain(args, e) })
		if code != 0 {
			t.Fatalf("seed %v: %d %s", args, code, out)
		}
		return out
	}
	if out := run(); !strings.Contains(out, "求人 12 件、利用者 1 人、園アカウント 2 件を入れました。") {
		t.Errorf("初回: %s", out)
	}
	if out := run(); !strings.Contains(out, "すでにデータがあります。入れ直すなら --reset を付けてください。") {
		t.Errorf("2 回目: %s", out)
	}
	if out := run("--extend"); !strings.Contains(out, "--extend: 求人 18 件を足しました") {
		t.Errorf("--extend: %s", out)
	}
	if out := run("--extend"); !strings.Contains(out, "--extend: 求人 0 件を足しました") {
		t.Errorf("--extend の 2 回目: %s", out)
	}
	if n := queryRows(t, url, "SELECT count(*) FROM recruits")[0][0]; n != "30" {
		t.Errorf("求人 %s 件", n)
	}
	if rows := queryRows(t, url, "SELECT count(*) FROM recruits WHERE demo_key IS NULL"); rows[0][0] != "0" {
		t.Error("demo_key が無い求人があります")
	}
	// 拡張の求人のうち、法人名が同じ園のアカウントがあるものだけが紐づく
	if rows := queryRows(t, url, "SELECT count(*) FROM recruits WHERE garden_account_id IS NOT NULL"); rows[0][0] == "0" {
		t.Error("園に紐づく求人が無い")
	}
	if out := run("--reset", "--extend"); !strings.Contains(out, "求人 30 件、利用者 1 人、園アカウント 2 件を入れました。") {
		t.Errorf("--reset --extend: %s", out)
	}
	if rows := queryRows(t, url, "SELECT min(id), max(id) FROM recruits"); rows[0][0] != "1" || rows[0][1] != "30" {
		t.Errorf("--reset は ID を 1 から振り直すはず: %v", rows)
	}
	// ログインできる（パスワードは argon2id）
	if rows := queryRows(t, url, "SELECT password_hash FROM garden_accounts LIMIT 1"); !verifyPassword(rows[0][0], demoPassword) {
		t.Error("ダミーのパスワードで検証できません")
	}
}

func TestSeedImagesIsIdempotent(t *testing.T) {
	app := newApp(t) // テンプレートの時点で 1 回流してある
	e := testEnvMap(map[string]string{"DATABASE_URL": app.url, "DEMO_DATA": "allow", "STORAGE_BACKEND": "local", "STORAGE_LOCAL_DIR": app.media, "IMAGE_PUBLIC_BASE_URL": "/media"})
	var code int
	before := app.queryInt("SELECT count(*) FROM images")
	out := captureStdout(t, func() { code = seedImagesMain([]string{"--source-dir", mediaSource}, e) })
	if code != 0 || !strings.Contains(out, "registered=0、linked=0、kept=24、no_recruit=0") || app.queryInt("SELECT count(*) FROM images") != before {
		t.Errorf("2 回目は何も増えないはず: %d %s", code, out)
	}
	// すでに登録済みの画像は、元のファイルを読まない
	if code := seedImagesMain([]string{"--source-dir", "/nonexistent"}, e); code != 0 {
		t.Errorf("登録済みなら、元のファイルが無くても成功するはず: %d", code)
	}
	if code := seedImagesMain(nil, e); code != 1 {
		t.Error("--source-dir も --source-url も無いとき: 1")
	}
	if code := seedImagesMain([]string{"--source-dir", "a", "--source-url", "https://x"}, e); code != 1 {
		t.Error("両方を指定したとき: 1")
	}
}

func TestReconcileAndCompleteUpload(t *testing.T) {
	app := newApp(t)
	s := &server{db: app.pool, store: app.srv.store, logf: func(string, ...any) {}, cfg: app.srv.cfg}
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour)
	put := func(key string, data []byte, mtime time.Time) {
		if err := s.store.Put(ctx, key, data, "image/png"); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(filepath.Join(app.media, "public", key), mtime, mtime)
	}
	// seed-images の実体は、テンプレートの一時フォルダーにあり、ここには無い。この DB の公開中の画像は、実体が無いだけ（reconcile は数えない種類）
	put("images/aaaaaaaa-0000-0000-0000-000000000001.png", []byte("orphan"), old)              // DB に行が無い
	put("images/aaaaaaaa-0000-0000-0000-000000000002.png", []byte("fresh orphan"), time.Now()) // 新しい（数えない）
	sum := sha256.Sum256([]byte("good"))
	app.exec(`INSERT INTO images (id, storage_key, mime_type, byte_size, width, height, sha256, alt, origin, status, status_changed_at)
		VALUES ('aaaaaaaa-0000-0000-0000-000000000003', 'images/aaaaaaaa-0000-0000-0000-000000000003.png', 'image/png', 4, 300, 300, $1, 'stuck ok', 'uploaded', 'uploading', now() - interval '3 hours')`, hex.EncodeToString(sum[:]))
	put("images/aaaaaaaa-0000-0000-0000-000000000003.png", []byte("good"), old)
	app.exec(`INSERT INTO images (id, storage_key, mime_type, byte_size, width, height, sha256, alt, origin, status, status_changed_at, demo_key)
		VALUES ('aaaaaaaa-0000-0000-0000-000000000004', 'images/aaaaaaaa-0000-0000-0000-000000000004.png', 'image/png', 4, 300, 300, $1, 'stuck bad', 'uploaded', 'uploading', now() - interval '3 hours', 'dk-bad')`, hex.EncodeToString(sum[:]))
	app.exec(`INSERT INTO images (id, storage_key, mime_type, byte_size, width, height, sha256, alt, origin, status, deleted_at)
		VALUES ('aaaaaaaa-0000-0000-0000-000000000005', 'images/aaaaaaaa-0000-0000-0000-000000000005.png', 'image/png', 4, 300, 300, $1, 'gone', 'uploaded', 'deleted', now())`, hex.EncodeToString(sum[:]))
	put("images/aaaaaaaa-0000-0000-0000-000000000005.png", []byte("stray"), old) // 削除済みなのに実体がある
	app.exec(`INSERT INTO images (id, storage_key, mime_type, byte_size, width, height, sha256, alt, origin, status, approved_at, created_at)
		VALUES ('aaaaaaaa-0000-0000-0000-000000000006', 'images/aaaaaaaa-0000-0000-0000-000000000006.png', 'image/png', 4, 300, 300, $1, 'unref', 'uploaded', 'approved', now(), now() - interval '3 hours')`, hex.EncodeToString(sum[:]))

	var lines []string
	counts, err := s.reconcile(ctx, 1, func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range counts {
		got[c.Name] = c.Count
	}
	// 登録の途中で止まっている 2 件（-03・-04）、DB に行の無い古い実体 1 件、削除済みの実体 1 件、どこからも使われていない公開中の画像 1 件
	// （seed の画像は、実体がこの保存先に無いので数えない。公開中で、求人から使われている）
	if got["stuck_uploading"] != 2 || got["orphan_objects"] != 1 || got["stray_objects"] != 1 || got["unreferenced"] != 1 {
		t.Errorf("reconcile: %v\n%s", got, strings.Join(lines, "\n"))
	}
	if app.queryInt("SELECT count(*) FROM images WHERE id = 'aaaaaaaa-0000-0000-0000-000000000006'") != 1 {
		t.Error("reconcile は、何も変えない")
	}
	// 登録の途中で止まった画像の片付け
	if st, err := s.completeUpload(ctx, "aaaaaaaa-0000-0000-0000-000000000003"); err != nil || st != "approved" {
		t.Errorf("実体と sha256 が合う: %s %v", st, err)
	}
	if st, err := s.completeUpload(ctx, "aaaaaaaa-0000-0000-0000-000000000004"); err != nil || st != "deleted" {
		t.Errorf("実体が無い: %s %v", st, err)
	}
	if app.queryInt("SELECT count(*) FROM images WHERE id = 'aaaaaaaa-0000-0000-0000-000000000004' AND demo_key IS NULL") != 1 {
		t.Error("deleted にしたら、demo_key を空けて、同じキーでやり直せるようにする")
	}
	if st, err := s.completeUpload(ctx, "aaaaaaaa-0000-0000-0000-000000000006"); err != nil || st != "approved" {
		t.Errorf("uploading でなければ、今の状態を返す: %s %v", st, err)
	}
	if _, err := s.completeUpload(ctx, "aaaaaaaa-0000-0000-0000-0000000000ff"); err == nil {
		t.Error("無い画像でエラーにならない")
	}
}

func TestImagesCLI(t *testing.T) {
	app := newApp(t)
	e := testEnvMap(map[string]string{"DATABASE_URL": app.url, "STORAGE_BACKEND": "local", "STORAGE_LOCAL_DIR": app.media, "IMAGE_PUBLIC_BASE_URL": "/media"})
	run := func(args ...string) (int, string) {
		var code int
		out := captureStdout(t, func() { code = imagesMain(args, e) })
		return code, out
	}
	file := filepath.Join(t.TempDir(), "x.jpg")
	if err := os.WriteFile(file, encodeJPEG(t, gradient(640, 480)), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := run("register", file, "--alt", "テスト画像", "--recruit-demo-key", "greenhouse-vine-grower", "--hero", "--demo-key", "cli-test-1")
	id := ""
	if i := strings.Index(out, "登録しました（公開中）: "); i >= 0 {
		id = strings.TrimSpace(out[i+len("登録しました（公開中）: "):])
	}
	if code != 0 || len(id) != 36 {
		t.Fatalf("register: %d %s", code, out)
	}
	if app.queryInt("SELECT count(*) FROM recruit_images ri JOIN recruits r ON r.id = ri.recruit_id WHERE ri.image_id = $1::uuid AND ri.is_hero AND r.demo_key = 'greenhouse-vine-grower'", id) != 1 {
		t.Error("ヒーローに指定して結ぶはず")
	}
	if c, o := run("register", file, "--alt", "x", "--recruit-id", "99999"); c != 1 || !strings.Contains(o, "求人が見つかりません") {
		t.Errorf("求人なし: %d %s", c, o)
	}
	if c, o := run("register", "/nonexistent.jpg", "--alt", "x", "--recruit-id", "1"); c != 1 || !strings.Contains(o, "ファイルを読めません") {
		t.Errorf("ファイルなし: %d %s", c, o)
	}
	if c, o := run("list", "--status", "approved"); c != 0 || !strings.Contains(o, id) || !strings.Contains(o, "640x480") || !strings.Contains(o, "テスト画像") {
		t.Errorf("list: %d %s", c, o)
	}
	if c, o := run("reject", id); c != 0 || !strings.Contains(o, "取り下げました（実体も消しました）") {
		t.Errorf("reject: %d %s", c, o)
	}
	if c, o := run("reject", id); c != 1 || !strings.Contains(o, "できませんでした: 取り下げできる状態の画像ではありません") {
		t.Errorf("reject（2 回目）: %d %s", c, o)
	}
	if c, o := run("list", "--status", "rejected"); c != 0 || !strings.Contains(o, id) || !strings.HasSuffix(strings.TrimSpace(o), "1 件") {
		t.Errorf("list rejected: %d %s", c, o)
	}
	if c, o := run("reconcile", "--stuck-hours", "0"); c != 0 || !strings.Contains(o, "報告だけです（何も変えていません）: stuck_uploading=0、orphan_objects=0、stray_objects=0、unreferenced=0") {
		t.Errorf("reconcile: %d %s", c, o)
	}
	if c, o := run("formfile"); c != 0 || !strings.Contains(o, "formfile に値のある求人: 0 件") {
		t.Errorf("formfile: %d %s", c, o)
	}
	if c, _ := run(); c != 2 {
		t.Error("引数なしは 2")
	}
	if c, _ := run("bogus"); c != 2 {
		t.Error("不明なサブコマンドは 2")
	}
}

// README の復旧手順（pg_dump → 別の DB へ pg_restore）で、データが戻ること。pg_restore は search_path を空にするので、
// 0009 の関数の中から別の関数を呼ぶときは public. を付ける（付けないと、バックアップから戻せない）。それをここで見張る。
// クライアントは /usr/lib/postgresql/17/bin に要る（CI は入れる。無いとスキップ）。
func TestBackupAndRestoreRoundtrip(t *testing.T) {
	const pgBin = "/usr/lib/postgresql/17/bin"
	dump, restore := pgBin+"/pg_dump", pgBin+"/pg_restore"
	if _, err := os.Stat(dump); err != nil {
		t.Skip("pg_dump が見つかりません")
	}
	ctx := context.Background()
	url := newEmptyDB(t)
	var sink strings.Builder
	if code, err := applyMigrations(ctx, url, false, &sink); err != nil || code != 0 {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, "INSERT INTO recruits (category, corporate, is_displayed, demo_key) VALUES ('復旧', '確認', true, 'restore-check')")
	conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "hj.dump")
	if out, err := exec.Command(dump, "-Fc", "-f", file, url).CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v\n%s", err, out)
	}
	name := "jobnavi_go_restored_" + itoa(time.Now().UnixNano()%1_000_000)
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	admin.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := pgx.Connect(context.Background(), adminURL); err == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(context.Background())
		}
	})
	restored := withDB(adminURL, name)
	if out, err := exec.Command(restore, "--no-owner", "-d", restored, file).CombinedOutput(); err != nil {
		t.Fatalf("pg_restore: %v\n%s", err, out)
	}
	if rows := queryRows(t, restored, "SELECT category FROM recruits WHERE demo_key = 'restore-check'"); len(rows) != 1 || rows[0][0] != "復旧" {
		t.Errorf("データが戻りません: %v", rows)
	}
	files, _ := migrationFiles()
	if rows := queryRows(t, restored, "SELECT version FROM schema_migrations ORDER BY 1"); len(rows) != len(files)+1 {
		t.Errorf("記録が戻りません: %v", rows)
	}
	if rows := queryRows(t, restored, "SELECT count(*) FROM recruits WHERE search_text LIKE search_pattern('復旧')"); rows[0][0] != "1" {
		t.Errorf("復旧のあとで、検索の関数が動きません: %v", rows)
	}
	sink.Reset()
	if code, err := applyMigrations(ctx, restored, false, &sink); err != nil || code != 0 || !strings.Contains(sink.String(), "未適用: なし") {
		t.Errorf("復旧した DB に migrate: %d %v\n%s", code, err, sink.String())
	}
}
