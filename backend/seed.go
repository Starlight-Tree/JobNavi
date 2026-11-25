package main

// ローカル用のダミーデータを入れる。backend/app/seed_demo.py と seed_images.py の移植。データは seed_data.go（Python のデータから生成）。実在の人・法人のデータは使わない。
//
//	api seed            求人・利用者・園アカウントが空のときだけ入れる
//	api seed --reset    求人・応募・アカウントを消して入れ直す（マスタは触らない）
//	api seed --extend   求人 seedExtraRecruits（18 件）のうち、demo_key がまだ無いものだけを足す（--reset と併用で 30 件構成）
//	api seed-images --source-dir DIR | --source-url https://…   ダミーの求人の画像を、通常の登録の流れ（検証 → 保存 → 公開中）で入れる。何度実行しても増えない
//
// DEMO_DATA=allow を渡した開発・テストの環境だけ。本番の環境には、この値を渡さない。

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const seedSourceMaxBytes = 20 * 1024 * 1024

// requireDemoDataAllowed は、ダミーデータの投入を、DEMO_DATA=allow を明示した環境だけにする。
func requireDemoDataAllowed(e env) bool {
	if e.str("DEMO_DATA") != "allow" {
		fmt.Println("ダミーデータの投入は、DEMO_DATA=allow を渡した開発・テストの環境だけです。実行しませんでした。")
		return false
	}
	return true
}

type seedExec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func insertSeedRecruit(ctx context.Context, tx seedExec, gardenID *int32, r seedRecruit, sch seedSchedule) error {
	comment, ok := seedComments[r.DemoKey]
	if !ok {
		comment = r.Category + "の仕事です。"
	}
	salary := 4
	if r.Employ == 1 {
		salary = 2
	}
	var rid int32
	err := tx.QueryRow(ctx,
		"INSERT INTO recruits (garden_account_id, employ, category, corporate, profile, place, salary, pay_min, pay_max, comment1, note1,"+
			" start_hour, start_minute, end_hour, end_minute, workday, insurance, is_displayed, demo_key)"+
			" VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, '連邦保険・園共済', $17, $18) RETURNING id",
		gardenID, int16(r.Employ), r.Category, r.Corporate, r.Corporate+"は、これは練習用の架空の法人です。", "翠環連邦内（架空の住所）",
		int16(salary), int32(r.PayMin), int32(r.PayMax), comment, payNote,
		sch.StartHour, sch.StartMinute, sch.EndHour, sch.EndMinute, sch.Workday, r.Shown, r.DemoKey).Scan(&rid)
	if err != nil {
		return err
	}
	for _, code := range r.Areas {
		if _, err := tx.Exec(ctx, "INSERT INTO recruit_areas VALUES ($1, $2)", rid, int32(code)); err != nil {
			return err
		}
	}
	for _, code := range r.Stations {
		if _, err := tx.Exec(ctx, "INSERT INTO recruit_stations VALUES ($1, $2)", rid, int32(code)); err != nil {
			return err
		}
	}
	for _, code := range r.Occupations {
		if _, err := tx.Exec(ctx, "INSERT INTO recruit_occupations VALUES ($1, $2)", rid, int32(code)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO recruit_employments VALUES ($1, $2)", rid, int32(r.Employ))
	return err
}

// addExtraRecruits は、seedExtraRecruits のうち、demo_key がまだ無い求人だけを足す。既存の求人・アカウント・画像との関連は変えない。何度実行しても同じ。
// 園のアカウントは作らない。seedGardens のメールのアカウントが DB にあるときだけ、法人名が同じ求人をそのアカウントに紐づける。
func addExtraRecruits(ctx context.Context, tx seedExec) (int, error) {
	keys := make([]string, len(seedExtraRecruits))
	for i, r := range seedExtraRecruits {
		keys[i] = r.DemoKey
	}
	rows, err := tx.Query(ctx, "SELECT demo_key FROM recruits WHERE demo_key = ANY($1)", keys)
	if err != nil {
		return 0, err
	}
	present := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, err
		}
		present[k] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	accountOf := map[string]int32{}
	for _, g := range seedGardens {
		var id int32
		switch err := tx.QueryRow(ctx, "SELECT id FROM garden_accounts WHERE email = $1", g.Email).Scan(&id); {
		case err == nil:
			accountOf[g.Corporate] = id
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return 0, err
		}
	}
	added := 0
	for _, r := range seedExtraRecruits {
		if present[r.DemoKey] {
			continue
		}
		var gid *int32
		if id, ok := accountOf[r.Corporate]; ok {
			gid = &id
		}
		if err := insertSeedRecruit(ctx, tx, gid, r, scheduleOfEmploy[r.Employ]); err != nil {
			return 0, err
		}
		added++
	}
	return added, nil
}

// backfillDemoKeys は、すでにある DB の求人に、固定の demo_key を一度だけ結ぶ。ID が seedRecruits の並び（1〜）と同じで、法人名と職種名も一致する行だけ。
// 名前を唯一のキーとして使うのではなく、「想定どおりの行か」の二重の確認に使う。一致しない行は触らない（demo_key が付かないので、画像も結ばれない）。
func backfillDemoKeys(ctx context.Context, tx seedExec) (int, error) {
	linked := 0
	for i, r := range seedRecruits {
		tag, err := tx.Exec(ctx, "UPDATE recruits SET demo_key = $1 WHERE id = $2 AND demo_key IS NULL AND corporate = $3 AND category = $4", r.DemoKey, int32(i+1), r.Corporate, r.Category)
		if err != nil {
			return 0, err
		}
		linked += int(tag.RowsAffected())
	}
	return linked, nil
}

func seedMain(args []string, e env) int {
	if !requireDemoDataAllowed(e) {
		return 1
	}
	has := func(f string) bool {
		for _, a := range args {
			if a == f {
				return true
			}
		}
		return false
	}
	reset, extend := has("--reset"), has("--extend")
	url := e.str("DATABASE_URL")
	if url == "" {
		fmt.Println("DATABASE_URL が未設定です")
		return 1
	}
	ctx := context.Background()
	pool, err := openPool(ctx, url, 2)
	if err != nil {
		fmt.Println("DB に接続できません")
		return 1
	}
	defer pool.Close()
	pwHash := hashPassword(demoPassword)
	msg, err := func() (string, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return "", err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if reset {
			if _, err := tx.Exec(ctx, "TRUNCATE entries, password_resets, recruits, users, user_sessions, garden_accounts, garden_sessions RESTART IDENTITY CASCADE"); err != nil {
				return "", err
			}
		} else {
			var n int64
			if err := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM recruits) + (SELECT count(*) FROM users)").Scan(&n); err != nil {
				return "", err
			}
			if n > 0 {
				linked, err := backfillDemoKeys(ctx, tx)
				if err != nil {
					return "", err
				}
				added := 0
				if extend {
					if added, err = addExtraRecruits(ctx, tx); err != nil {
						return "", err
					}
				}
				text := "すでにデータがあります。入れ直すなら --reset を付けてください。"
				if linked > 0 {
					text += fmt.Sprintf("（求人 %d 件に demo_key を結びました）", linked)
				}
				if extend {
					text += fmt.Sprintf("\n--extend: 求人 %d 件を足しました（demo_key がすでにある求人は触りません）。", added)
				}
				return text, tx.Commit(ctx)
			}
		}
		gardenIDs := make([]int32, 0, len(seedGardens))
		for _, g := range seedGardens {
			var id int32
			if err := tx.QueryRow(ctx, "INSERT INTO garden_accounts (corporate, email, password_hash) VALUES ($1, $2, $3) RETURNING id", g.Corporate, g.Email, pwHash).Scan(&id); err != nil {
				return "", err
			}
			gardenIDs = append(gardenIDs, id)
		}
		for _, r := range seedRecruits {
			var gid *int32
			if idx, ok := seedGardenOfCorporate[r.Corporate]; ok {
				gid = &gardenIDs[idx]
			}
			if err := insertSeedRecruit(ctx, tx, gid, r, defaultSchedule); err != nil {
				return "", err
			}
		}
		if _, err := tx.Exec(ctx, "INSERT INTO users (display_name, email, password_hash) VALUES ('ひかり', 'hoshiniwa-user@example.com', $1)", pwHash); err != nil {
			return "", err
		}
		added := 0
		if extend {
			if added, err = addExtraRecruits(ctx, tx); err != nil {
				return "", err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("求人 %d 件、利用者 1 人、園アカウント %d 件を入れました。", len(seedRecruits)+added, len(seedGardens)), nil
	}()
	if err != nil {
		fmt.Printf("失敗しました（%T）。DB への接続か SQL の実行でエラーです\n", err)
		return 1
	}
	fmt.Println(msg)
	return 0
}

// ---- ダミーの画像 ----

type demoImage struct {
	DemoKey        string `json:"demo_key"`
	RecruitDemoKey string `json:"recruit_demo_key"`
	File           string `json:"file"`
	Sha256         string `json:"sha256"`
	Alt            string `json:"alt"`
	Position       int32  `json:"position"`
	Hero           bool   `json:"hero"`
}

func readSeedSource(entry demoImage, dir, url string) ([]byte, error) {
	var data []byte
	if dir != "" {
		var err error
		if data, err = os.ReadFile(filepath.Join(dir, entry.File)); err != nil {
			return nil, err
		}
	} else {
		if !strings.HasPrefix(url, "https://") {
			return nil, errors.New("--source-url は https:// で始めてください")
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(strings.TrimRight(url, "/") + "/" + entry.File)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s を取得できません", entry.File)
		}
		if data, err = io.ReadAll(io.LimitReader(resp.Body, seedSourceMaxBytes+1)); err != nil {
			return nil, err
		}
	}
	if len(data) > seedSourceMaxBytes {
		return nil, fmt.Errorf("%s が大きすぎます", entry.File)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != entry.Sha256 {
		return nil, fmt.Errorf("%s の sha256 がマニフェストと一致しません。別の画像かもしれないので、使いません", entry.File)
	}
	return data, nil
}

//go:embed demo_images.json
var demoImagesJSON []byte

// seedImages は、マニフェスト（demo_images.json）の demo_key で、画像と求人を結ぶ。何度実行しても、画像も関連も増えない。
//   - 元のファイルは、マニフェストの sha256 と一致しなければ使わない（別の画像が別の求人に結ばれるのを防ぐ）
//   - 求人は recruits.demo_key で先に探す。見つからなければ、画像も登録せずに知らせる
//   - すでに結ばれている関連（園が変えた並び・ヒーロー）は、再実行で変えない
func (s *server) seedImages(ctx context.Context, dir, url string, out func(string)) (map[string]int, error) {
	var manifest struct {
		Images []demoImage `json:"images"`
	}
	if err := json.Unmarshal(demoImagesJSON, &manifest); err != nil {
		return nil, err
	}
	stats := map[string]int{"registered": 0, "linked": 0, "kept": 0, "no_recruit": 0}
	for _, entry := range manifest.Images {
		var recruitID int32
		if err := s.db.QueryRow(ctx, "SELECT id FROM recruits WHERE demo_key = $1", entry.RecruitDemoKey).Scan(&recruitID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			stats["no_recruit"]++
			out(fmt.Sprintf("求人 %s がありません。画像 %s は、求人ができてから登録します", entry.RecruitDemoKey, entry.DemoKey))
			continue
		}
		var imageID, status string
		err := s.db.QueryRow(ctx, "SELECT id::text, status FROM images WHERE demo_key = $1", entry.DemoKey).Scan(&imageID, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			data, err := readSeedSource(entry, dir, url)
			if err != nil {
				return nil, err
			}
			p, err := processImage(data, seedSourceMaxBytes)
			if err != nil {
				return nil, err
			}
			demoKey := entry.DemoKey
			imageID, err = s.registerImage(ctx, p, entry.Alt, "generated", nil, &demoKey)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // 同時に動いた別の実行が先に登録した
				if err := s.db.QueryRow(ctx, "SELECT id::text, status FROM images WHERE demo_key = $1", entry.DemoKey).Scan(&imageID, &status); err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			} else {
				stats["registered"]++
				status = "approved"
			}
		case err != nil:
			return nil, err
		}
		if status == "uploading" { // 前の実行が登録の途中で止まった。実体が揃っていれば公開中にし、無ければ消してやり直す
			if status, err = s.completeUpload(ctx, imageID); err != nil {
				return nil, err
			}
			if status == "deleted" {
				out(fmt.Sprintf("登録の途中で止まっていた画像を片付けました（%s）。もう一度実行してください", entry.DemoKey))
				continue
			}
		}
		if status != "approved" {
			out(fmt.Sprintf("状態 %s の画像です（%s）。触りません", status, entry.DemoKey))
			continue
		}
		tag, err := s.db.Exec(ctx, "INSERT INTO recruit_images (recruit_id, image_id, position) VALUES ($1, $2::uuid, $3) ON CONFLICT (recruit_id, image_id) DO NOTHING", recruitID, imageID, entry.Position)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() > 0 {
			stats["linked"]++
			if entry.Hero {
				var one int
				switch err := s.db.QueryRow(ctx, "SELECT 1 FROM recruit_images WHERE recruit_id = $1 AND is_hero", recruitID).Scan(&one); {
				case errors.Is(err, pgx.ErrNoRows):
					if _, err := s.db.Exec(ctx, "UPDATE recruit_images SET is_hero = true WHERE recruit_id = $1 AND image_id = $2::uuid", recruitID, imageID); err != nil {
						return nil, err
					}
				case err != nil:
					return nil, err
				}
			}
		} else {
			stats["kept"]++
		}
	}
	return stats, nil
}

func seedImagesMain(args []string, e env) int {
	if !requireDemoDataAllowed(e) {
		return 1
	}
	var dir, url string
	for i, a := range args {
		if i+1 < len(args) && a == "--source-dir" {
			dir = args[i+1]
		}
		if i+1 < len(args) && a == "--source-url" {
			url = args[i+1]
		}
	}
	if (dir == "") == (url == "") {
		fmt.Println("--source-dir か --source-url の、どちらか一方を指定してください")
		return 1
	}
	s, closeFn, err := newToolServer(e)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer closeFn()
	stats, err := s.seedImages(context.Background(), dir, url, func(msg string) { fmt.Println(msg) })
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Printf("画像: registered=%d、linked=%d、kept=%d、no_recruit=%d\n", stats["registered"], stats["linked"], stats["kept"], stats["no_recruit"])
	return 0
}
