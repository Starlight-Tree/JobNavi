package main

// 運営用の画像の操作（登録・取り下げ・点検）と、道具（CLI）の共通の部品。backend/app/images_cli.py と image_service.py（complete_upload・reject_image・reconcile）の移植。
// DB と保存先の認証情報（環境変数）を持つ運営だけが実行できる。園の API からは呼べない。
//
//	api images list [--status rejected]
//	api images register FILE --alt "代替テキスト" (--recruit-id N | --recruit-demo-key KEY) [--hero] [--demo-key KEY]   登録した時点で公開される
//	api images reject IMAGE_ID          公開後の取り下げ（実体も消す）。承認の操作ではない
//	api images reconcile [--stuck-hours 1]   DB と保存先の食い違いを報告するだけ（何も変えない）
//	api images formfile                 recruits.formfile（画像ファイル名の列）に値のある求人の一覧

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// newToolServer は、道具（CLI）が使う、HTTP を持たない server（DB・保存先・ログだけ）。
func newToolServer(e env) (*server, func(), error) {
	cfg, err := loadConfig(e)
	if err != nil {
		return nil, nil, fmt.Errorf("設定エラー: %w", err)
	}
	ctx := context.Background()
	pool, err := openPool(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return nil, nil, errors.New("DB に接続できません")
	}
	st, err := newStorage(cfg)
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("設定エラー: %w", err)
	}
	return &server{cfg: cfg, db: pool, store: st, logf: func(string, ...any) {}}, pool.Close, nil
}

// completeUpload は、登録の途中で止まった画像（uploading）を片付ける。実体があり sha256 が一致すれば公開中にし、無ければ（または違えば）deleted にして実体を消す。
// 戻り値は新しい状態（approved / deleted）。uploading でなければ何もせず、今の状態を返す。
func (s *server) completeUpload(ctx context.Context, imageID string) (string, error) {
	var status, key, sum string
	err := s.db.QueryRow(ctx, "SELECT status, storage_key, sha256 FROM images WHERE id = $1::uuid", imageID).Scan(&status, &key, &sum)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &imageConflict{"画像が見つかりません"}
	}
	if err != nil {
		return "", err
	}
	if status != "uploading" {
		return status, nil
	}
	ok := false
	if exists, err := s.store.Exists(ctx, key); err == nil && exists {
		if data, err := s.store.Get(ctx, key); err == nil {
			h := sha256.Sum256(data)
			ok = hex.EncodeToString(h[:]) == sum
		}
	}
	if ok {
		if _, err := s.db.Exec(ctx, "UPDATE images SET status = 'approved', approved_at = now(), status_changed_at = now() WHERE id = $1::uuid AND status = 'uploading'", imageID); err != nil {
			return "", err
		}
		return "approved", nil
	}
	// demo_key は空けて、同じキーでやり直せるようにする
	if _, err := s.db.Exec(ctx, "UPDATE images SET status = 'deleted', status_changed_at = now(), deleted_at = now(), demo_key = NULL WHERE id = $1::uuid AND status = 'uploading'", imageID); err != nil {
		return "", err
	}
	s.deleteObject(ctx, &key)
	return "deleted", nil
}

// rejectImage は運営の取り下げ（公開後の安全弁）。DB を先に rejected にし（ヒーローの印も外れる）、実体を消す。
func (s *server) rejectImage(ctx context.Context, imageID string) error {
	var key string
	err := s.db.QueryRow(ctx,
		"UPDATE images SET status = 'rejected', status_changed_at = now() WHERE id = $1::uuid AND status IN ('pending', 'publishing', 'approved') RETURNING storage_key", imageID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return &imageConflict{"取り下げできる状態の画像ではありません"}
	}
	if err != nil {
		return err
	}
	return s.store.Delete(ctx, key) // 失敗しても、DB は rejected で公開されない。残った実体は reconcile が報告する
}

// reconcile は DB とストレージの食い違いを探して報告する。何も変えない（削除も、状態の変更もしない）。
// 検出するもの: stuck_uploading（登録が途中で止まっている）、orphan_objects（DB に行のない実体）、stray_objects（取り下げ・削除済みなのに残っている実体）、
// unreferenced（どこからも使われていない公開中の画像）。登録・公開の最中かもしれないので、新しいもの（10 分以内、unreferenced と stuck は stuckHours 以内）は数えない。
func (s *server) reconcile(ctx context.Context, stuckHours int, out func(string)) ([4]struct {
	Name  string
	Count int
}, error) {
	counts := [4]struct {
		Name  string
		Count int
	}{{"stuck_uploading", 0}, {"orphan_objects", 0}, {"stray_objects", 0}, {"unreferenced", 0}}
	now := time.Now().UTC()
	stuckBefore := now.Add(-time.Duration(stuckHours) * time.Hour)

	rows, err := s.db.Query(ctx, "SELECT id::text FROM images WHERE status = 'uploading' AND status_changed_at < $1", stuckBefore)
	if err != nil {
		return counts, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return counts, err
	}
	for _, id := range ids {
		counts[0].Count++
		out(fmt.Sprintf("検出: 登録が途中で止まっている画像 %s", id))
	}

	rows, err = s.db.Query(ctx, "SELECT storage_key, status FROM images")
	if err != nil {
		return counts, err
	}
	known := map[string]string{}
	for rows.Next() {
		var k, st string
		if err := rows.Scan(&k, &st); err != nil {
			rows.Close()
			return counts, err
		}
		known[k] = st
	}
	if err := rows.Err(); err != nil {
		return counts, err
	}
	keys, err := s.store.ListKeys(ctx, "images/")
	if err != nil {
		return counts, err
	}
	for _, k := range keys {
		if k.Modified.After(now.Add(-10 * time.Minute)) {
			continue // 登録の最中かもしれない
		}
		status, ok := known[k.Key]
		switch {
		case !ok:
			counts[1].Count++
			out(fmt.Sprintf("検出: DB に行のない実体 %s", k.Key))
		case status != "approved" && status != "uploading":
			counts[2].Count++
			out(fmt.Sprintf("検出: 公開されないはずの実体が残っています %s（状態 %s）", k.Key, status))
		}
	}

	rows, err = s.db.Query(ctx,
		"SELECT i.id::text FROM images i WHERE i.status = 'approved' AND NOT EXISTS (SELECT 1 FROM recruit_images ri WHERE ri.image_id = i.id) "+
			"AND NOT EXISTS (SELECT 1 FROM garden_images gi WHERE gi.image_id = i.id) AND i.created_at < $1", stuckBefore)
	if err != nil {
		return counts, err
	}
	if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return counts, err
	}
	for _, id := range ids {
		counts[3].Count++
		out(fmt.Sprintf("検出: どこからも使われていない公開中の画像 %s", id))
	}
	return counts, nil
}

func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func imagesMain(args []string, e env) int {
	if len(args) == 0 {
		fmt.Println("usage: api images {list,register,reject,reconcile,formfile} …")
		return 2
	}
	cmd, rest := args[0], args[1:]
	s, closeFn, err := newToolServer(e)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer closeFn()
	ctx := context.Background()
	out := func(msg string) { fmt.Println(msg) }
	fail := func(err error) int {
		var rej *imageRejected
		var conflict *imageConflict
		var se *storageError
		switch {
		case errors.As(err, &rej):
			fmt.Printf("できませんでした: %s\n", rej.msg)
		case errors.As(err, &conflict):
			fmt.Printf("できませんでした: %s\n", conflict.msg)
		case errors.As(err, &se):
			fmt.Printf("できませんでした: %s\n", se.msg)
		default:
			fmt.Printf("DB のエラーです（%T）\n", err)
		}
		return 1
	}
	switch cmd {
	case "list":
		status, hasStatus := flagValue(rest, "--status")
		var filter *string
		if hasStatus {
			filter = &status
		}
		rows, err := s.db.Query(ctx,
			"SELECT id::text, status, origin, width, height, alt, (SELECT count(*) FROM recruit_images ri WHERE ri.image_id = images.id) AS recruits "+
				"FROM images WHERE ($1::text IS NULL OR status = $1::text) ORDER BY created_at", filter)
		if err != nil {
			return fail(err)
		}
		n := 0
		for rows.Next() {
			var id, st, origin, alt string
			var w, h int32
			var recruits int64
			if err := rows.Scan(&id, &st, &origin, &w, &h, &alt, &recruits); err != nil {
				rows.Close()
				return fail(err)
			}
			out(fmt.Sprintf("%s  %-10s %-9s %dx%d  求人%d件  %s", id, st, origin, w, h, recruits, alt))
			n++
		}
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		out(fmt.Sprintf("%d 件", n))
	case "register":
		if len(rest) == 0 {
			fmt.Println("usage: api images register FILE --alt TEXT (--recruit-id N | --recruit-demo-key KEY) [--hero] [--demo-key KEY]")
			return 2
		}
		file := rest[0]
		alt, hasAlt := flagValue(rest, "--alt")
		idArg, hasID := flagValue(rest, "--recruit-id")
		demoArg, hasDemo := flagValue(rest, "--recruit-demo-key")
		if !hasAlt || hasID == hasDemo {
			fmt.Println("usage: api images register FILE --alt TEXT (--recruit-id N | --recruit-demo-key KEY) [--hero] [--demo-key KEY]")
			return 2
		}
		var recruitID int32
		if hasID {
			n, err := strconv.ParseInt(idArg, 10, 32)
			if err != nil {
				fmt.Println("--recruit-id は整数にしてください")
				return 2
			}
			err = s.db.QueryRow(ctx, "SELECT id FROM recruits WHERE id = $1", int32(n)).Scan(&recruitID)
			if errors.Is(err, pgx.ErrNoRows) {
				out("求人が見つかりません")
				return 1
			} else if err != nil {
				return fail(err)
			}
		} else {
			err := s.db.QueryRow(ctx, "SELECT id FROM recruits WHERE demo_key = $1", demoArg).Scan(&recruitID)
			if errors.Is(err, pgx.ErrNoRows) {
				out("求人が見つかりません")
				return 1
			} else if err != nil {
				return fail(err)
			}
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			fmt.Printf("ファイルを読めません: %s\n", file)
			return 1
		}
		p, err := processImage(raw, s.cfg.MaxUploadBytes)
		if err != nil {
			return fail(err)
		}
		var demoKey *string
		if v, ok := flagValue(rest, "--demo-key"); ok {
			demoKey = &v
		}
		imageID, err := s.registerImage(ctx, p, alt, "generated", nil, demoKey)
		if err != nil {
			return fail(err)
		}
		if _, err := s.db.Exec(ctx, "INSERT INTO recruit_images (recruit_id, image_id, position) SELECT $1::int, $2::uuid, COALESCE(MAX(position) + 1, 0) FROM recruit_images WHERE recruit_id = $1::int", recruitID, imageID); err != nil {
			return fail(err)
		}
		if hasFlag(rest, "--hero") {
			if _, err := s.db.Exec(ctx, "UPDATE recruit_images SET is_hero = (image_id = $1::uuid) WHERE recruit_id = $2", imageID, recruitID); err != nil {
				return fail(err)
			}
		}
		out(fmt.Sprintf("登録しました（公開中）: %s", imageID))
	case "reject":
		if len(rest) != 1 {
			fmt.Println("usage: api images reject IMAGE_ID")
			return 2
		}
		id, ok := parseUUIDLoose(rest[0])
		if !ok {
			fmt.Printf("api images reject: error: argument image_id: invalid UUID value: '%s'\n", rest[0])
			return 2
		}
		if err := s.rejectImage(ctx, id); err != nil {
			return fail(err)
		}
		out("取り下げました（実体も消しました）")
	case "reconcile":
		hours := 1
		if v, ok := flagValue(rest, "--stuck-hours"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				fmt.Println("--stuck-hours は整数にしてください")
				return 2
			}
			hours = n
		}
		counts, err := s.reconcile(ctx, hours, out)
		if err != nil {
			return fail(err)
		}
		line := "報告だけです（何も変えていません）: "
		for i, c := range counts {
			if i > 0 {
				line += "、"
			}
			line += fmt.Sprintf("%s=%d", c.Name, c.Count)
		}
		out(line)
	case "formfile":
		rows, err := s.db.Query(ctx, "SELECT id, formfile FROM recruits WHERE formfile IS NOT NULL AND formfile <> '' ORDER BY id")
		if err != nil {
			return fail(err)
		}
		n := 0
		for rows.Next() {
			var id int32
			var f string
			if err := rows.Scan(&id, &f); err != nil {
				rows.Close()
				return fail(err)
			}
			out(fmt.Sprintf("求人 %d: %s", id, f))
			n++
		}
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		out(fmt.Sprintf("formfile に値のある求人: %d 件", n))
	default:
		fmt.Println("usage: api images {list,register,reject,reconcile,formfile} …")
		return 2
	}
	return 0
}
