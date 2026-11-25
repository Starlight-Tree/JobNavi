package main

// 園が自分の求人・法人の画像を扱う API と、ローカル保存のときの公開画像の配信（/media）。backend/app/images_api.py の移植。
//
// 権限: 園は、自分に紐づく求人（recruits.garden_account_id）と、自分の画像だけを扱える。確認は、画面ではなくここ（API）で、毎回 SQL の条件で行う。
// 他の園の求人・画像は、存在しないのと同じ 404 を返す。
// 園ができるのは「登録（保存した時点で公開）・並べ替え・ヒーローの指定・外す（その場で削除）」まで。承認の段階は無い。
// 公開後に問題があれば、運営だけが取り下げられる（運営用 CLI の reject）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	maxProfileImages     = 5
	detailNoImage        = "画像が見つかりません"
	detailBadImageRef    = "画像の指定が正しくありません"
	detailStoreUnavail   = "画像を保存できませんでした。しばらくしてから、もう一度お試しください"
	detailAltRange       = "代替テキストは 1〜300 文字で入れてください"
	detailUnsupportedImg = "JPEG・PNG・WebP の画像だけ登録できます"
	detailMimeMismatch   = "画像の種類が、指定と一致しません"
)

var rolePattern = regexp.MustCompile(`^(logo|banner)$`)

type imageBody struct {
	ID       string  `json:"id"`
	Alt      string  `json:"alt"`
	Width    int32   `json:"width"`
	Height   int32   `json:"height"`
	MimeType string  `json:"mime_type"`
	Origin   string  `json:"origin"`
	Status   string  `json:"status"` // approved（公開中）/ rejected（運営が取り下げた）
	Position int32   `json:"position"`
	IsHero   bool    `json:"is_hero"`
	Role     *string `json:"role"`
	URL      *string `json:"url"` // approved（公開中）のときだけ公開 URL。それ以外は null
}

type gardenRecruitImages struct {
	RecruitID   int32       `json:"recruit_id"`
	Category    *string     `json:"category"`
	IsDisplayed bool        `json:"is_displayed"`
	MaxImages   int         `json:"max_images"`
	Images      []imageBody `json:"images"`
}

type recruitRef struct {
	ID          int32
	Category    *string
	IsDisplayed bool
}

// scanImageBody は、i.id::text, i.storage_key, i.alt, i.width, i.height, i.mime_type, i.origin, i.status, position の順に読み、
// extra（is_hero・role）を後ろに続ける。
func (s *server) scanImageBody(row pgx.Row, withHero, withRole bool) (imageBody, error) {
	var (
		b   imageBody
		key string
	)
	dest := []any{&b.ID, &key, &b.Alt, &b.Width, &b.Height, &b.MimeType, &b.Origin, &b.Status, &b.Position}
	if withHero {
		dest = append(dest, &b.IsHero)
	}
	if withRole {
		dest = append(dest, &b.Role)
	}
	if err := row.Scan(dest...); err != nil {
		return b, err
	}
	if b.Status == "approved" {
		u, err := s.publicURL(key)
		if err != nil {
			return b, err
		}
		b.URL = &u
	}
	return b, nil
}

// recruitImagesForGarden は、園が見る、求人の画像（取り下げられたものを含む。削除済みは除く）。
func (s *server) recruitImagesForGarden(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, recruitID int32) ([]imageBody, error) {
	rows, err := q.Query(ctx,
		"SELECT i.id::text, i.storage_key, i.alt, i.width, i.height, i.mime_type, i.origin, i.status, ri.position, ri.is_hero FROM recruit_images ri "+
			"JOIN images i ON i.id = ri.image_id WHERE ri.recruit_id = $1 AND i.status <> 'deleted' ORDER BY ri.position", recruitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []imageBody{}
	for rows.Next() {
		b, err := s.scanImageBody(rows, true, false)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ownRecruit は、自分の求人。他の園の求人・無い求人は 404。lock のときは行をロックする（同じ求人への同時の操作を直列にする）。
func (s *server) ownRecruit(ctx context.Context, q rowQuerier, recruitID, gardenID int32, lock bool) (recruitRef, *httpError) {
	var r recruitRef
	sql := "SELECT id, category, is_displayed FROM recruits WHERE id = $1 AND garden_account_id = $2"
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, recruitID, gardenID).Scan(&r.ID, &r.Category, &r.IsDisplayed)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	if err != nil {
		return r, s.dbFailure(err)
	}
	return r, nil
}

func (s *server) gardenImagesResponse(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, rec recruitRef) (gardenRecruitImages, error) {
	images, err := s.recruitImagesForGarden(ctx, q, rec.ID)
	return gardenRecruitImages{RecruitID: rec.ID, Category: rec.Category, IsDisplayed: rec.IsDisplayed, MaxImages: maxImagesPerRecruit, Images: images}, err
}

// recruitIDParam は {id}（int4 に収まる整数）。範囲外は、どの求人でもない（ok=false → 404）。
func recruitIDParam(v *validation, r *http.Request) (id int32, inRange bool) {
	n := v.intParam([]any{"path", "recruit_id"}, r.PathValue("id"), nil, nil)
	if n == nil {
		return 0, false
	}
	if !n.IsInt64() || n.Int64() < math.MinInt32 || n.Int64() > math.MaxInt32 {
		return 0, false
	}
	return int32(n.Int64()), true
}

func (s *server) listRecruitImages(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	id, inRange := recruitIDParam(&v, r)
	if e := v.err(); e != nil {
		return e
	}
	if !inRange {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	rec, e := s.ownRecruit(ctx, s.db, id, g.ID, false)
	if e != nil {
		return e
	}
	out, err := s.gardenImagesResponse(ctx, s.db, rec)
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, out, nil)
	return nil
}

// readUpload は、本文を上限まで読み、検証して（メタデータを除いた）配信用のバイト列にする。
func (s *server) readUpload(r *http.Request, alt string) (processed, string, *httpError) {
	alt = strings.TrimFunc(alt, isPySpace)
	if n := len([]rune(alt)); n < 1 || n > 300 {
		return processed{}, "", newHTTPError(http.StatusUnprocessableEntity, detailAltRange)
	}
	declared := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if declared != "image/jpeg" && declared != "image/png" && declared != "image/webp" {
		return processed{}, "", newHTTPError(http.StatusUnsupportedMediaType, detailUnsupportedImg)
	}
	limit := s.cfg.MaxUploadBytes
	tooLarge := newHTTPError(http.StatusRequestEntityTooLarge, fmt.Sprintf("画像は %dMB 以下にしてください", limit/(1024*1024)))
	if cl := r.Header.Get("Content-Length"); cl != "" && strings.Trim(cl, "0123456789") == "" {
		if n, ok := parseDecimal(cl); !ok || n > limit {
			return processed{}, "", tooLarge
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return processed{}, "", newHTTPError(http.StatusBadRequest, "There was an error parsing the body")
	}
	if int64(len(raw)) > limit {
		return processed{}, "", tooLarge
	}
	p, err := processImage(raw, limit)
	if err != nil {
		var rej *imageRejected
		if errors.As(err, &rej) {
			return processed{}, "", newHTTPError(http.StatusUnprocessableEntity, rej.msg)
		}
		return processed{}, "", internalError
	}
	if p.mime != declared {
		return processed{}, "", newHTTPError(http.StatusUnprocessableEntity, detailMimeMismatch)
	}
	return p, alt, nil
}

func parseDecimal(s string) (int64, bool) {
	var n int64
	for _, c := range s {
		if n > math.MaxInt64/10-1 {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// register は registerImage の失敗を HTTP のエラーにする（保存先の失敗は 503）。
func (s *server) register(ctx context.Context, g garden, p processed, alt string) (string, *httpError) {
	id, err := s.registerImage(ctx, p, alt, "uploaded", &g.ID, nil)
	if err == nil {
		return id, nil
	}
	var se *storageError
	var rej *imageRejected
	switch {
	case errors.As(err, &se):
		return "", newHTTPError(http.StatusServiceUnavailable, detailStoreUnavail)
	case errors.As(err, &rej):
		return "", newHTTPError(http.StatusUnprocessableEntity, rej.msg)
	}
	return "", s.dbFailure(err)
}

func tooManyImages() *httpError {
	return newHTTPError(http.StatusConflict, fmt.Sprintf("1 つの求人に登録できる画像は %d 枚までです", maxImagesPerRecruit))
}

// requiredQuery は必須のクエリ（無ければ 422 missing）。
func requiredQuery(v *validation, r *http.Request, name string) (string, bool) {
	raw, ok := lastValue(r.URL.Query(), name)
	if !ok {
		v.issues = append(v.issues, issue{Type: "missing", Loc: []any{"query", name}, Msg: "Field required", Input: nil})
		return "", false
	}
	return raw, true
}

// uploadRecruitImage は、本文が画像そのもの（Content-Type は image/jpeg・image/png・image/webp）。検証に通れば、保存した時点で公開される。
func (s *server) uploadRecruitImage(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	id, inRange := recruitIDParam(&v, r)
	alt, _ := requiredQuery(&v, r, "alt")
	if e := v.err(); e != nil {
		return e
	}
	if !inRange {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	// 所有権と枚数を、重い処理の前に確かめる
	if _, e := s.ownRecruit(ctx, s.db, id, g.ID, false); e != nil {
		return e
	}
	var count int64
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM recruit_images WHERE recruit_id = $1", id).Scan(&count); err != nil {
		return s.dbFailure(err)
	}
	if count >= maxImagesPerRecruit {
		return tooManyImages()
	}
	p, text, e := s.readUpload(r, alt)
	if e != nil {
		return e
	}
	if _, e := s.ownRecruit(ctx, s.db, id, g.ID, false); e != nil {
		return e
	}
	imageID, e := s.register(ctx, g, p, text)
	if e != nil {
		return e
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return s.dbFailure(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e := s.ownRecruit(ctx, tx, id, g.ID, true); e != nil { // 同じ求人への同時の登録で、表示順が重ならないように
		return e
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM recruit_images WHERE recruit_id = $1", id).Scan(&count); err != nil {
		return s.dbFailure(err)
	}
	if count >= maxImagesPerRecruit {
		key, err := unlinkIfUnused(ctx, tx, imageID) // 補償: 結べなかった画像を、その場で消す
		if err != nil {
			return s.dbFailure(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return s.dbFailure(err)
		}
		s.deleteObject(ctx, key)
		return tooManyImages()
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO recruit_images (recruit_id, image_id, position) SELECT $1::int, $2::uuid, COALESCE(MAX(position) + 1, 0) FROM recruit_images WHERE recruit_id = $1::int", id, imageID); err != nil {
		return s.dbFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	images, err := s.recruitImagesForGarden(ctx, s.db, id)
	if err != nil {
		return s.dbFailure(err)
	}
	for _, b := range images {
		if b.ID == imageID {
			writeJSON(w, http.StatusCreated, b, nil)
			return nil
		}
	}
	return internalError
}

// ---- 並べ替え・ヒーロー ----

func (s *server) reorderRecruitImages(w http.ResponseWriter, r *http.Request) *httpError {
	body, e := readJSONBody(r)
	if e != nil {
		return e
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	id, inRange := recruitIDParam(&v, r)
	var ids []string
	obj, e := bodyObject(body)
	if e != nil {
		v.issues = append(v.issues, e.issues...)
	} else if raw, present := obj["image_ids"]; !present {
		v.issues = append(v.issues, issue{Type: "missing", Loc: []any{"body", "image_ids"}, Msg: "Field required", Input: obj})
	} else if list, ok := raw.([]any); !ok {
		v.issues = append(v.issues, issue{Type: "list_type", Loc: []any{"body", "image_ids"}, Msg: "Input should be a valid list", Input: raw})
	} else {
		for i, item := range list {
			if str, ok := item.(string); ok {
				ids = append(ids, str)
			} else {
				v.issues = append(v.issues, issue{Type: "string_type", Loc: []any{"body", "image_ids", i}, Msg: "Input should be a valid string", Input: item})
			}
		}
	}
	if e := v.err(); e != nil {
		return e
	}
	wanted := make([]string, 0, len(ids))
	for _, raw := range ids {
		u, ok := parseUUIDLoose(raw)
		if !ok {
			return newHTTPError(http.StatusUnprocessableEntity, detailBadImageRef)
		}
		wanted = append(wanted, u)
	}
	if !inRange {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return s.dbFailure(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rec, e := s.ownRecruit(ctx, tx, id, g.ID, true)
	if e != nil {
		return e
	}
	rows, err := tx.Query(ctx, "SELECT image_id::text FROM recruit_images WHERE recruit_id = $1", id)
	if err != nil {
		return s.dbFailure(err)
	}
	current, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return s.dbFailure(err)
	}
	if !sameSet(wanted, current) {
		return newHTTPError(http.StatusUnprocessableEntity, "並べ替える画像が、今の画像の一覧と一致しません")
	}
	for position, imageID := range wanted {
		if _, err := tx.Exec(ctx, "UPDATE recruit_images SET position = $1::int WHERE recruit_id = $2 AND image_id = $3::uuid", int32(position), id, imageID); err != nil {
			return s.dbFailure(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	out, err := s.gardenImagesResponse(ctx, s.db, rec)
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, out, nil)
	return nil
}

// sameSet は、wanted に重複が無く、current と同じ集合か。
func sameSet(wanted, current []string) bool {
	if len(wanted) != len(current) {
		return false
	}
	seen := map[string]bool{}
	for _, w := range wanted {
		if seen[w] {
			return false
		}
		seen[w] = true
	}
	for _, c := range current {
		if !seen[c] {
			return false
		}
	}
	return true
}

func (s *server) setHeroImage(w http.ResponseWriter, r *http.Request) *httpError {
	body, e := readJSONBody(r)
	if e != nil {
		return e
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	id, inRange := recruitIDParam(&v, r)
	var imageRef string
	if obj, e := bodyObject(body); e != nil {
		v.issues = append(v.issues, e.issues...)
	} else {
		imageRef, _ = stringField(obj, "image_id", &v.issues)
	}
	if e := v.err(); e != nil {
		return e
	}
	imageID, ok := parseUUIDLoose(imageRef)
	if !ok {
		return newHTTPError(http.StatusUnprocessableEntity, detailBadImageRef)
	}
	if !inRange {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return s.dbFailure(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rec, e := s.ownRecruit(ctx, tx, id, g.ID, true)
	if e != nil {
		return e
	}
	var status string
	err = tx.QueryRow(ctx, "SELECT i.status FROM recruit_images ri JOIN images i ON i.id = ri.image_id WHERE ri.recruit_id = $1 AND ri.image_id = $2::uuid", id, imageID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return newHTTPError(http.StatusNotFound, detailNoImage)
	}
	if err != nil {
		return s.dbFailure(err)
	}
	if status != "approved" {
		return newHTTPError(http.StatusConflict, "公開されていない（取り下げられた）画像は、ヒーローにできません")
	}
	if _, err := tx.Exec(ctx, "UPDATE recruit_images SET is_hero = false WHERE recruit_id = $1 AND is_hero", id); err != nil {
		return s.dbFailure(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE recruit_images SET is_hero = true WHERE recruit_id = $1 AND image_id = $2::uuid", id, imageID); err != nil {
		return s.dbFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	out, err := s.gardenImagesResponse(ctx, s.db, rec)
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, out, nil)
	return nil
}

// removeRecruitImage は削除。求人から外し、どこからも使われなければ、その場で画像を消す（元に戻せない）。
func (s *server) removeRecruitImage(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	id, inRange := recruitIDParam(&v, r)
	imageID, _ := v.pathUUID("image_id", r.PathValue("image_id"))
	if e := v.err(); e != nil {
		return e
	}
	if !inRange {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return s.dbFailure(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e := s.ownRecruit(ctx, tx, id, g.ID, true); e != nil {
		return e
	}
	gone, err := tx.Exec(ctx, "DELETE FROM recruit_images WHERE recruit_id = $1 AND image_id = $2::uuid", id, imageID)
	if err != nil {
		return s.dbFailure(err)
	}
	if gone.RowsAffected() == 0 {
		return newHTTPError(http.StatusNotFound, detailNoImage)
	}
	key, err := unlinkIfUnused(ctx, tx, imageID)
	if err != nil {
		return s.dbFailure(err)
	}
	if _, err := tx.Exec(ctx,
		"UPDATE recruit_images r SET position = s.rn - 1 FROM (SELECT image_id, row_number() OVER (ORDER BY position) AS rn FROM recruit_images WHERE recruit_id = $1) s "+
			"WHERE r.recruit_id = $1 AND r.image_id = s.image_id", id); err != nil {
		return s.dbFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	s.deleteObject(ctx, key) // DB の確定のあとで実体を消す。失敗しても、画像は DB の上で消えている
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// removeImageNamed は、/images/order・/images/hero に DELETE が来たとき（FastAPI は {image_id} の経路に回って、UUID の検査で 422 にする）。
func (s *server) removeImageNamed(name string) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) *httpError {
		r.SetPathValue("image_id", name)
		return s.removeRecruitImage(w, r)
	}
}

// ---- 法人の画像（ロゴ・バナー）。同じ画像の基盤を使う ----

func (s *server) listProfileImages(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	rows, err := s.db.Query(ctx,
		"SELECT i.id::text, i.storage_key, i.alt, i.width, i.height, i.mime_type, i.origin, i.status, gi.position, gi.role FROM garden_images gi "+
			"JOIN images i ON i.id = gi.image_id WHERE gi.garden_account_id = $1 AND i.status <> 'deleted' ORDER BY gi.role, gi.position", g.ID)
	if err != nil {
		return s.dbFailure(err)
	}
	defer rows.Close()
	out := []imageBody{}
	for rows.Next() {
		b, err := s.scanImageBody(rows, false, true)
		if err != nil {
			return s.dbFailure(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, out, nil)
	return nil
}

func (s *server) uploadProfileImage(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	alt, _ := requiredQuery(&v, r, "alt")
	role, hasRole := requiredQuery(&v, r, "role")
	if hasRole && !rolePattern.MatchString(role) {
		v.issues = append(v.issues, issue{Type: "string_pattern_mismatch", Loc: []any{"query", "role"}, Msg: "String should match pattern '^(logo|banner)$'", Input: role, Ctx: map[string]any{"pattern": "^(logo|banner)$"}})
	}
	if e := v.err(); e != nil {
		return e
	}
	p, text, e := s.readUpload(r, alt)
	if e != nil {
		return e
	}
	// 枚数の確認（法人の行をロックして数える）。画像の登録は、この確認のトランザクションを閉じてから行う
	// （images の行は garden_accounts を参照するので、ロックを持ったまま登録すると、登録が待たされて止まる）
	var count int64
	if e := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT id FROM garden_accounts WHERE id = $1 FOR UPDATE", g.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT count(*) FROM garden_images WHERE garden_account_id = $1 AND role = $2", g.ID, role).Scan(&count)
	}); e != nil {
		return s.dbFailure(e)
	}
	if count >= maxProfileImages {
		return newHTTPError(http.StatusConflict, fmt.Sprintf("登録できる画像は、種類ごとに %d 枚までです", maxProfileImages))
	}
	imageID, e := s.register(ctx, g, p, text)
	if e != nil {
		return e
	}
	if e := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT id FROM garden_accounts WHERE id = $1 FOR UPDATE", g.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			"INSERT INTO garden_images (garden_account_id, image_id, role, position) SELECT $1::int, $2::uuid, $3, COALESCE(MAX(position) + 1, 0) FROM garden_images "+
				"WHERE garden_account_id = $1::int AND role = $3", g.ID, imageID, role)
		return err
	}); e != nil {
		return s.dbFailure(e)
	}
	b, err := s.scanImageBody(s.db.QueryRow(ctx,
		"SELECT i.id::text, i.storage_key, i.alt, i.width, i.height, i.mime_type, i.origin, i.status, gi.position, gi.role FROM garden_images gi "+
			"JOIN images i ON i.id = gi.image_id WHERE gi.image_id = $1::uuid", imageID), false, true)
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusCreated, b, nil)
	return nil
}

func (s *server) removeProfileImage(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	var v validation
	imageID, _ := v.pathUUID("image_id", r.PathValue("image_id"))
	if e := v.err(); e != nil {
		return e
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return s.dbFailure(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	gone, err := tx.Exec(ctx, "DELETE FROM garden_images WHERE garden_account_id = $1 AND image_id = $2::uuid", g.ID, imageID)
	if err != nil {
		return s.dbFailure(err)
	}
	if gone.RowsAffected() == 0 {
		return newHTTPError(http.StatusNotFound, detailNoImage)
	}
	key, err := unlinkIfUnused(ctx, tx, imageID)
	if err != nil {
		return s.dbFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	s.deleteObject(ctx, key)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- ローカル保存のときだけ、画像を配る（S3 / R2 では、公開 URL の配信元が配る）----

var mediaTypes = map[string]string{"jpg": "image/jpeg", "png": "image/png", "webp": "image/webp"}

func (s *server) media(w http.ResponseWriter, r *http.Request) *httpError {
	key := strings.TrimPrefix(r.URL.Path, "/media/")
	ext := ""
	if i := strings.LastIndex(key, "."); i >= 0 {
		ext = key[i+1:]
	}
	mime, ok := mediaTypes[ext]
	if !ok {
		return newHTTPError(http.StatusNotFound, detailNoImage)
	}
	data, err := s.store.Get(r.Context(), key)
	if err != nil {
		return newHTTPError(http.StatusNotFound, detailNoImage)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, bytes.NewReader(data))
	return nil
}

// inTx は fn を 1 つのトランザクションで行う。fn が失敗したら巻き戻す。
func (s *server) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
