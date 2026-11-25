package main

// 画像の読み取り（公開 API 用）。backend/app/image_queries.py の移植。DB の行から、公開 URL などを組み立てる。
// 公開 URL は、保存キーから組み立てる（署名付きではない）。保存キーの形は storage.go の checkKey。

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errBadKey = errors.New("保存キーの形が正しくありません")

var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9/_.-]{0,200}$`)

func checkKey(key string) (string, error) {
	if !keyPattern.MatchString(key) || strings.Contains(key, "..") || strings.Contains(key, "//") || strings.HasSuffix(key, "/") {
		return "", errBadKey
	}
	return key, nil
}

func (s *server) publicURL(key string) (string, error) {
	k, err := checkKey(key)
	if err != nil {
		return "", err
	}
	return s.cfg.PublicBaseURL + "/" + k, nil
}

type imageInfo struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Alt      string `json:"alt"`
	Width    int32  `json:"width"`
	Height   int32  `json:"height"`
	MimeType string `json:"mime_type"`
}

func (s *server) scanImageInfo(row pgx.Row, extra ...any) (imageInfo, error) {
	var (
		info imageInfo
		key  string
	)
	dest := append([]any{&info.ID, &key, &info.Alt, &info.Width, &info.Height, &info.MimeType}, extra...)
	if err := row.Scan(dest...); err != nil {
		return info, err
	}
	u, err := s.publicURL(key)
	if err != nil {
		return info, err
	}
	info.URL = u
	return info, nil
}

// publicImage は、求人の公開してよい（approved の）画像 1 枚と、ヒーローか。
type publicImage struct {
	info   imageInfo
	isHero bool
}

// publicImages は、求人の公開してよい画像を、表示順に。
func (s *server) publicImages(ctx context.Context, recruitID int32) ([]publicImage, error) {
	rows, err := s.db.Query(ctx,
		"SELECT i.id::text, i.storage_key, i.alt, i.width, i.height, i.mime_type, ri.is_hero FROM recruit_images ri "+
			"JOIN images i ON i.id = ri.image_id WHERE ri.recruit_id = $1 AND i.status = 'approved' ORDER BY ri.position", recruitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []publicImage{}
	for rows.Next() {
		var p publicImage
		info, err := s.scanImageInfo(rows, &p.isHero)
		if err != nil {
			return nil, err
		}
		p.info = info
		out = append(out, p)
	}
	return out, rows.Err()
}

// heroImages は、求人ごとの代表画像（一覧のサムネ用）。1 回の SQL で、渡した求人の分を引く。
// 選び方は詳細 API と同じ: 公開してよい画像のうち、ヒーローに指定されたもの。無ければ表示順で先頭。公開中の画像が 1 枚も無い求人は、結果に入らない。
func (s *server) heroImages(ctx context.Context, ids []int32) (map[int32]imageInfo, error) {
	out := map[int32]imageInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		"SELECT DISTINCT ON (ri.recruit_id) i.id::text, i.storage_key, i.alt, i.width, i.height, i.mime_type, ri.recruit_id "+
			"FROM recruit_images ri JOIN images i ON i.id = ri.image_id "+
			"WHERE ri.recruit_id = ANY($1::int[]) AND i.status = 'approved' "+
			"ORDER BY ri.recruit_id, ri.is_hero DESC, ri.position, ri.image_id", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid int32
		info, err := s.scanImageInfo(rows, &rid)
		if err != nil {
			return nil, err
		}
		out[rid] = info
	}
	return out, rows.Err()
}
