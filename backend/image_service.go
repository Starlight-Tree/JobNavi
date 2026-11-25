package main

// 画像の登録・削除（API・CLI・seed が同じものを使う）。backend/app/image_service.py の移植。
//
// 登録（registerImage）: 検証（processImage）→ DB に uploading で記録 → 保存 → approved（公開中）。承認の段階は無く、登録が済んだ画像はそのまま公開される。
// 削除（unlinkIfUnused）: 求人などから外れて、どこからも使われなくなった画像は、その場で deleted にして実体を消す（猶予は無い）。
// どの途中で止まっても、DB とストレージが食い違ったままにならないための設計:
//   - 先に DB へ意図（uploading）を書き、そのあとストレージを触る。削除は、先に DB を deleted / rejected にして確定し、そのあとで実体を消す
//   - ストレージの操作は何度やっても同じ結果になる。実体の削除に失敗しても、DB が先に確定しているので、公開 API には出ない（残った実体は reconcile が報告する）
//   - 画像の更新は、同じキーに上書きせず、新しい画像（新しい uuid のキー）として登録する
//   - 参照されている画像（recruit_images / garden_images）は消さない。DB も RESTRICT で守る
// 各段階は自動コミット（pgxpool の 1 文ごと）か、呼び出し側のトランザクション（unlinkIfUnused だけ）。

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const maxImagesPerRecruit = 10

// errImageConflict は、状態が合わず、操作できない（取り下げられた画像をヒーローにする、など）。メッセージは固定の説明。
type imageConflict struct{ msg string }

func (e *imageConflict) Error() string { return e.msg }

// newUUID は UUID v4（小文字の 8-4-4-4-12）。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("乱数を読めません")
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// registerImage は、検証済みの画像を保存して、公開中（approved）で登録する。失敗したら、作りかけの行と実体を消して例外を返す。
func (s *server) registerImage(ctx context.Context, p processed, alt, origin string, ownerGardenID *int32, demoKey *string) (string, error) {
	alt = strings.TrimSpace(alt)
	if n := utf8.RuneCountInString(alt); n < 1 || n > 300 {
		return "", reject("代替テキストは 1〜300 文字で入れてください")
	}
	id := newUUID()
	key := fmt.Sprintf("images/%s.%s", id, p.ext)
	if _, err := s.db.Exec(ctx,
		"INSERT INTO images (id, storage_key, mime_type, byte_size, width, height, sha256, alt, origin, owner_garden_account_id, demo_key, status) "+
			"VALUES ($1::uuid, $2, $3, $4::int, $5::int, $6::int, $7, $8, $9, $10::int, $11, 'uploading')",
		id, key, p.mime, int32(len(p.data)), int32(p.width), int32(p.height), p.sha256, alt, origin, ownerGardenID, demoKey); err != nil {
		return "", err
	}
	err := s.store.Put(ctx, key, p.data, p.mime)
	if err == nil {
		_, err = s.db.Exec(ctx, "UPDATE images SET status = 'approved', approved_at = now(), status_changed_at = now() WHERE id = $1::uuid AND status = 'uploading'", id)
	}
	if err != nil {
		// 補償: 作りかけの行と実体を消す。消せなければ uploading のまま残り、reconcile が報告する
		cleanup := context.WithoutCancel(ctx)
		if derr := s.store.Delete(cleanup, key); derr == nil {
			_, derr = s.db.Exec(cleanup, "DELETE FROM images WHERE id = $1::uuid AND status = 'uploading'", id)
			if derr != nil {
				s.logf("登録の途中で失敗し、片付けも失敗しました。reconcile が報告します image_id=%s", id)
			}
		} else {
			s.logf("登録の途中で失敗し、片付けも失敗しました。reconcile が報告します image_id=%s", id)
		}
		return "", err
	}
	return id, nil
}

// unlinkIfUnused は、どこからも参照されなくなった画像を deleted にして、消す実体の保存キーを返す（参照が残っていれば nil）。
// コミットはしない。呼び出し側が、求人から外す操作と一緒に commit したあとで、deleteObject で実体を消す。
func unlinkIfUnused(ctx context.Context, q queryRower, imageID string) (*string, error) {
	var key string
	err := q.QueryRow(ctx,
		"UPDATE images SET status = 'deleted', status_changed_at = now(), deleted_at = now(), deletion_requested_at = now() "+
			"WHERE id = $1::uuid AND status <> 'deleted' "+
			"AND NOT EXISTS (SELECT 1 FROM recruit_images WHERE image_id = $1::uuid) AND NOT EXISTS (SELECT 1 FROM garden_images WHERE image_id = $1::uuid) RETURNING storage_key",
		imageID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &key, nil
}

// deleteObject は DB の確定のあとで実体を消す。失敗しても例外にしない（利用者の操作は成功しており、残った実体は reconcile が報告する）。
func (s *server) deleteObject(ctx context.Context, key *string) {
	if key == nil {
		return
	}
	if err := s.store.Delete(context.WithoutCancel(ctx), *key); err != nil {
		s.logf("画像の実体を消せませんでした。reconcile が報告します")
	}
}
