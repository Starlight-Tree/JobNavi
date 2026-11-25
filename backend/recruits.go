package main

// 求人の検索・詳細・条件ごとの件数・マスタ。backend/app/recruits.py と schemas.py の移植。
// 検索の規則: 同じ種類の条件の中は OR（配列が重なれば一致）、種類の間は AND。キーワード（q）も AND。表示中の求人だけ。
// キーワードは、空白（全角も）で区切った語がすべて、検索用の文字列（recruit_search.search_text）に含まれる求人（語の間は AND）。
// 正規化（search_fold）は DB の関数 1 か所（0009）。SEARCH_ENGINE=meilisearch のときは、語が候補の id（関連度の順）に置き換わる。
// エンジンに届かない・結果が多すぎるときは、LIKE で探す（Postgres が正）。

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	keywordMaxLength = 100
	keywordMaxTerms  = 5
	detailNoRecruit  = "求人が見つかりません"
)

// 表示用の名前は、検索用ビュー（recruit_search）の配列の番号から、マスタの名前を引いて付ける
const namesSQL = `
  ARRAY(SELECT a.name FROM areas a WHERE a.code = ANY(s.area_codes) ORDER BY a.sort) AS areas,
  ARRAY(SELECT t.name FROM stations t WHERE t.code = ANY(s.station_codes) ORDER BY t.sort) AS stations,
  ARRAY(SELECT o.name FROM occupations o WHERE o.code = ANY(s.occupation_codes) ORDER BY o.sort) AS occupations,
  ARRAY(SELECT e.name FROM employments e WHERE e.code = ANY(s.employment_codes) ORDER BY e.sort) AS employments
`

const summaryCols = "s.id, s.corporate, s.category, s.salary, s.pay_min, s.pay_max, s.comment1, s.note1, s.formfile"

// 条件の種類: (応答での名前, 検索用ビューの配列の列, クエリの名前)
var kinds = []struct{ name, column, key string }{
	{"areas", "area_codes", "area"},
	{"stations", "station_codes", "station"},
	{"occupations", "occupation_codes", "occupation"},
	{"employments", "employment_codes", "employment"},
}

// ---- 応答の型（pydantic のモデルと同じ項目の順）----

type recruitSummary struct {
	ID          int32      `json:"id"`
	Corporate   *string    `json:"corporate"`
	Category    *string    `json:"category"`
	Salary      *int16     `json:"salary"` // 1 年俸制 / 2 月給制 / 3 日給制 / 4 時給制
	PayMin      *int32     `json:"pay_min"`
	PayMax      *int32     `json:"pay_max"`
	Comment1    *string    `json:"comment1"`
	Note1       *string    `json:"note1"`
	Formfile    *string    `json:"formfile"`
	Areas       []string   `json:"areas"`
	Stations    []string   `json:"stations"`
	Occupations []string   `json:"occupations"`
	Employments []string   `json:"employments"`
	HeroImage   *imageInfo `json:"hero_image"`
}

type recruitDetail struct {
	recruitSummary
	Images    []imageInfo `json:"images"`
	Profile   *string     `json:"profile"`
	Place     *string     `json:"place"`
	Note2     *string     `json:"note2"`
	Note3     *string     `json:"note3"`
	Comment2  *string     `json:"comment2"`
	Comment3  *string     `json:"comment3"`
	Workday   *string     `json:"workday"`
	Insurance *string     `json:"insurance"`
	StartTime *string     `json:"start_time"`
	EndTime   *string     `json:"end_time"`
}

type recruitPage struct {
	Total    int64            `json:"total"`
	Page     int64            `json:"page"`
	PerPage  int64            `json:"per_page"`
	LastPage int64            `json:"last_page"`
	Items    []recruitSummary `json:"items"`
}

type facetCount struct {
	Code  int32 `json:"code"`
	Count int32 `json:"count"`
}

type facets struct {
	Total       int64        `json:"total"`
	Areas       []facetCount `json:"areas"`
	Stations    []facetCount `json:"stations"`
	Occupations []facetCount `json:"occupations"`
	Employments []facetCount `json:"employments"`
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// scanSummary は、summaryCols と namesSQL の順に読む。extra は、その後ろの列。
func scanSummary(row pgx.Row, extra ...any) (recruitSummary, error) {
	var r recruitSummary
	dest := append([]any{&r.ID, &r.Corporate, &r.Category, &r.Salary, &r.PayMin, &r.PayMax, &r.Comment1, &r.Note1, &r.Formfile}, extra...)
	dest = append(dest, &r.Areas, &r.Stations, &r.Occupations, &r.Employments)
	if err := row.Scan(dest...); err != nil {
		return r, err
	}
	r.Areas, r.Stations, r.Occupations, r.Employments = nz(r.Areas), nz(r.Stations), nz(r.Occupations), nz(r.Employments)
	return r, nil
}

// ---- クエリの読み取り（FastAPI の Query と同じ順に誤りを集める）----

type searchParams struct {
	sel      map[string][]int64
	q        string
	page     int64
	perPage  int64
	overflow bool // int64 に収まらない値（Python では DB のエラーになり 500）
}

func lastValue(vals map[string][]string, key string) (string, bool) {
	v := vals[key]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// readSearchParams は area station occupation employment q（と、withPaging のとき page per_page）を読む。誤りは宣言の順にまとめて返す。
func readSearchParams(r *http.Request, withPaging bool) (searchParams, *httpError) {
	vals := r.URL.Query()
	var v validation
	p := searchParams{sel: map[string][]int64{}, page: 1, perPage: 10}
	for _, k := range kinds {
		for i, raw := range vals[k.key] {
			n := v.intParam([]any{"query", k.key, i}, raw, nil, nil)
			if n == nil {
				continue
			}
			if !n.IsInt64() {
				p.overflow = true
				continue
			}
			p.sel[k.key] = append(p.sel[k.key], n.Int64())
		}
	}
	if raw, ok := lastValue(vals, "q"); ok {
		if utf8.RuneCountInString(raw) > keywordMaxLength {
			v.issues = append(v.issues, issue{Type: "string_too_long", Loc: []any{"query", "q"}, Msg: fmt.Sprintf("String should have at most %d characters", keywordMaxLength), Input: raw, Ctx: map[string]any{"max_length": keywordMaxLength}})
		} else {
			p.q = raw
		}
	}
	if withPaging {
		if raw, ok := lastValue(vals, "page"); ok {
			if n := v.intParam([]any{"query", "page"}, raw, i64(1), nil); n != nil {
				if n.IsInt64() {
					p.page = n.Int64()
				} else {
					p.overflow = true
				}
			}
		}
		if raw, ok := lastValue(vals, "per_page"); ok {
			if n := v.intParam([]any{"query", "per_page"}, raw, i64(1), i64(50)); n != nil {
				p.perPage = n.Int64()
			}
		}
	}
	if e := v.err(); e != nil {
		return p, e
	}
	if p.overflow {
		return p, internalError
	}
	return p, nil
}

// isPySpace は Python の str.split() が区切りにする文字（空白類）。
func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// keywordTerms は、キーワードを語に分ける。空白（全角を含む）で区切り、重複を除き、先頭から keywordMaxTerms 語まで。
func keywordTerms(q string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range strings.FieldsFunc(q, isPySpace) {
		if !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	if len(terms) > keywordMaxTerms {
		terms = terms[:keywordMaxTerms]
	}
	return terms
}

// engineIDs は、キーワードの候補の id（関連度の順）を、検索エンジンから引く。使わない設定・語が無い・エンジンを使えないときは (nil, false)。
// 語は、求人の側と同じ DB の関数 search_fold を通してから渡す。リクエストごとに 1 回だけ呼ぶ。エンジンの返事を待つ間は、DB の接続を持たない。
func (s *server) engineIDs(ctx context.Context, q string) ([]int64, bool, error) {
	terms := keywordTerms(q)
	if len(terms) == 0 || s.engine == nil {
		return nil, false, nil
	}
	rows, err := s.db.Query(ctx, "SELECT search_fold(t) FROM unnest($1::text[]) WITH ORDINALITY AS u(t, n) ORDER BY n", terms)
	if err != nil {
		return nil, false, err
	}
	folded, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, false, err
	}
	if !usableTerms(folded) {
		return nil, false, nil
	}
	ids, err := s.engine.searchIDs(ctx, strings.Join(folded, " "))
	if err != nil {
		s.logf("検索エンジンを使えないので、Postgres の検索に切り替えました: %v", err) // 本文は固定の文（URL・鍵を含まない）
		return nil, false, nil
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, true, nil
}

type whereClause struct {
	sql    string
	args   []any
	idsRef string // 候補の id の位置パラメーター（無ければ空）
}

func (w *whereClause) bind(v any) string {
	w.args = append(w.args, v)
	return fmt.Sprintf("$%d", len(w.args))
}

// buildWhere は検索の条件。skip に種類の名前を渡すと、その種類の条件は使わない（件数の数え方に使う。キーワードは、いつも使う）。
// ids（検索エンジンの候補。空でもよい）があれば、キーワードは id で絞る（LIKE は使わない）。
func buildWhere(sel map[string][]int64, skip string, q string, ids []int64, hasIDs bool) whereClause {
	var w whereClause
	conds := []string{"s.is_displayed"}
	for _, k := range kinds {
		if values := sel[k.key]; len(values) > 0 && k.name != skip {
			conds = append(conds, fmt.Sprintf("s.%s && %s::int[]", k.column, w.bind(values)))
		}
	}
	if hasIDs {
		w.idsRef = w.bind(ids)
		conds = append(conds, fmt.Sprintf("s.id = ANY(%s::int[])", w.idsRef))
	} else {
		for _, term := range keywordTerms(q) {
			// 語は、生のまま渡す。DB の search_pattern が、正規化してから % _ \ をエスケープして（ワイルドカードにせず）、%語% にする
			conds = append(conds, fmt.Sprintf("s.search_text LIKE search_pattern(%s::text)", w.bind(term)))
		}
	}
	w.sql = strings.Join(conds, " AND ")
	return w
}

// ---- API ----

type code struct {
	Code int32  `json:"code"`
	Name string `json:"name"`
}

type occupationGroup struct {
	Category string `json:"category"`
	Items    []code `json:"items"`
}

type master struct {
	Areas       []code            `json:"areas"`
	Stations    []code            `json:"stations"`
	Occupations []occupationGroup `json:"occupations"`
	Employments []code            `json:"employments"`
}

func (s *server) codes(ctx context.Context, table string) ([]code, error) {
	rows, err := s.db.Query(ctx, "SELECT code, name FROM "+table+" WHERE NOT is_deleted ORDER BY sort") // テーブル名は固定の文字列
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (code, error) {
		var c code
		return c, r.Scan(&c.Code, &c.Name)
	})
	if out == nil {
		out = []code{}
	}
	return out, err
}

func (s *server) master(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	var m master
	var err error
	if m.Areas, err = s.codes(ctx, "areas"); err != nil {
		return s.dbFailure(err)
	}
	if m.Stations, err = s.codes(ctx, "stations"); err != nil {
		return s.dbFailure(err)
	}
	rows, err := s.db.Query(ctx, "SELECT code, category, name FROM occupations WHERE NOT is_deleted ORDER BY sort")
	if err != nil {
		return s.dbFailure(err)
	}
	m.Occupations = []occupationGroup{}
	for rows.Next() {
		var c code
		var category string
		if err := rows.Scan(&c.Code, &category, &c.Name); err != nil {
			rows.Close()
			return s.dbFailure(err)
		}
		if n := len(m.Occupations); n == 0 || m.Occupations[n-1].Category != category {
			m.Occupations = append(m.Occupations, occupationGroup{Category: category, Items: []code{}})
		}
		g := &m.Occupations[len(m.Occupations)-1]
		g.Items = append(g.Items, c)
	}
	if err := rows.Err(); err != nil {
		return s.dbFailure(err)
	}
	if m.Employments, err = s.codes(ctx, "employments"); err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, m, nil)
	return nil
}

// dbFailure は DB の失敗を 500 にする。例外の文字列は応答にも出さない（ログには型だけ）。
func (s *server) dbFailure(err error) *httpError {
	s.logf("DB の問い合わせに失敗しました (%T)", err)
	return internalError
}

func (s *server) facets(w http.ResponseWriter, r *http.Request) *httpError {
	p, e := readSearchParams(r, false)
	if e != nil {
		return e
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	ids, hasIDs, err := s.engineIDs(ctx, p.q)
	if err != nil {
		return s.dbFailure(err)
	}
	where := buildWhere(p.sel, "", p.q, ids, hasIDs)
	var out facets
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM recruit_search s WHERE "+where.sql, where.args...).Scan(&out.Total); err != nil {
		return s.dbFailure(err)
	}
	counts := map[string][]facetCount{}
	for _, k := range kinds {
		others := buildWhere(p.sel, k.name, p.q, ids, hasIDs)
		rows, err := s.db.Query(ctx,
			fmt.Sprintf("SELECT c AS code, count(*)::int AS count FROM recruit_search s, unnest(s.%s) AS c WHERE %s GROUP BY c ORDER BY c", k.column, others.sql),
			others.args...)
		if err != nil {
			return s.dbFailure(err)
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (facetCount, error) {
			var f facetCount
			return f, r.Scan(&f.Code, &f.Count)
		})
		if err != nil {
			return s.dbFailure(err)
		}
		if list == nil {
			list = []facetCount{}
		}
		counts[k.name] = list
	}
	out.Areas, out.Stations, out.Occupations, out.Employments = counts["areas"], counts["stations"], counts["occupations"], counts["employments"]
	writeJSON(w, http.StatusOK, out, nil)
	return nil
}

func (s *server) searchRecruits(w http.ResponseWriter, r *http.Request) *httpError {
	p, e := readSearchParams(r, true)
	if e != nil {
		return e
	}
	// OFFSET が int64 を超える（Python では DB のエラーになる）値は、DB に渡さず 500
	if p.page-1 > math.MaxInt64/p.perPage {
		return internalError
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	ids, hasIDs, err := s.engineIDs(ctx, p.q)
	if err != nil {
		return s.dbFailure(err)
	}
	where := buildWhere(p.sel, "", p.q, ids, hasIDs)
	// エンジンの候補があるときは、関連度の順（候補の並び）。無いときは id の順
	order := "s.id"
	if hasIDs {
		order = fmt.Sprintf("array_position(%s::int[], s.id)", where.idsRef)
	}
	var total int64
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM recruit_search s WHERE "+where.sql, where.args...).Scan(&total); err != nil {
		return s.dbFailure(err)
	}
	limit, offset := where.bind(p.perPage), where.bind((p.page-1)*p.perPage)
	rows, err := s.db.Query(ctx,
		fmt.Sprintf("SELECT %s, %s FROM recruit_search s WHERE %s ORDER BY %s LIMIT %s OFFSET %s", summaryCols, namesSQL, where.sql, order, limit, offset),
		where.args...)
	if err != nil {
		return s.dbFailure(err)
	}
	items := []recruitSummary{}
	idList := []int32{}
	for rows.Next() {
		item, err := scanSummary(rows)
		if err != nil {
			rows.Close()
			return s.dbFailure(err)
		}
		items = append(items, item)
		idList = append(idList, item.ID)
	}
	if err := rows.Err(); err != nil {
		return s.dbFailure(err)
	}
	heroes, err := s.heroImages(ctx, idList) // 画像は、この 1 ページの分をまとめて 1 回で引く
	if err != nil {
		return s.dbFailure(err)
	}
	for i := range items {
		if h, ok := heroes[items[i].ID]; ok {
			hero := h
			items[i].HeroImage = &hero
		}
	}
	last := (total + p.perPage - 1) / p.perPage
	if last < 1 {
		last = 1
	}
	writeJSON(w, http.StatusOK, recruitPage{Total: total, Page: p.page, PerPage: p.perPage, LastPage: last, Items: items}, nil)
	return nil
}

func (s *server) recruitDetail(w http.ResponseWriter, r *http.Request) *httpError {
	var v validation
	n := v.intParam([]any{"path", "recruit_id"}, r.PathValue("id"), nil, nil)
	if e := v.err(); e != nil {
		return e
	}
	// int4 の範囲を外れる id は、どの求人でもない
	if !n.IsInt64() || n.Int64() < math.MinInt32 || n.Int64() > math.MaxInt32 {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	id := int32(n.Int64())
	ctx, cancel := dbContext(r)
	defer cancel()
	var d recruitDetail
	row := s.db.QueryRow(ctx,
		fmt.Sprintf("SELECT %s, s.profile, s.place, s.note2, s.note3, s.comment2, s.comment3, s.workday, s.insurance, "+
			"NULLIF(concat_ws(':', s.start_hour, s.start_minute), '') AS start_time, "+
			"NULLIF(concat_ws(':', s.end_hour, s.end_minute), '') AS end_time, %s "+
			"FROM recruit_search s WHERE s.id = $1 AND s.is_displayed", summaryCols, namesSQL), id)
	var err error
	d.recruitSummary, err = scanSummary(row, &d.Profile, &d.Place, &d.Note2, &d.Note3, &d.Comment2, &d.Comment3, &d.Workday, &d.Insurance, &d.StartTime, &d.EndTime)
	if err != nil {
		if err == pgx.ErrNoRows {
			return newHTTPError(http.StatusNotFound, detailNoRecruit)
		}
		return s.dbFailure(err)
	}
	images, err := s.publicImages(ctx, id)
	if err != nil {
		return s.dbFailure(err)
	}
	d.Images = make([]imageInfo, 0, len(images))
	for _, im := range images {
		d.Images = append(d.Images, im.info)
	}
	// ヒーローに指定された画像。無ければ表示順で先頭。画像が 1 枚も無ければ null
	for i, im := range images {
		if im.isHero {
			hero := images[i].info
			d.HeroImage = &hero
			break
		}
	}
	if d.HeroImage == nil && len(images) > 0 {
		hero := images[0].info
		d.HeroImage = &hero
	}
	writeJSON(w, http.StatusOK, d, nil)
	return nil
}
