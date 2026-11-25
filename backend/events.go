package main

// 閲覧の記録と、それを使った「関連する求人」「あなたへのおすすめ」。backend/app/recruit_events.py と recruit_embeddings.py の移植。
// 機械学習は使わない（ルールと統計だけ。理由を説明できる順位づけ）。順位づけは rankRelated・rankRecommendations に集めてあり、
// 求人の ID を、並べた順に返すだけ（画面の形を作る部分とは分けてある）。説明は docs/recommendation.md。
//   - POST /api/recruits/{id}/view: 求職者の閲覧を記録する（認証は /api/auth/me と同じ Bearer。利用者は JWT の sub から決まる）
//   - GET /api/recruits/{id}/related: 公開。その求人と一緒に見られた求人・条件の近い求人
//   - GET /api/recommendations: 求職者だけ。見た求人から、まだ見ていない求人を選ぶ

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	dedupMinutes       = 30 // 同じ利用者・同じ求人の閲覧は、この分数のあいだ 1 件だけ記録する
	cooccurrenceWeight = 3  // おすすめの得点: 共起の数 × 3
	embeddingWeight    = 5  // 埋め込みの類似度（0〜1）× 5。埋め込みが無ければ 0 を足すだけ
)

// 条件の一致を数える種類（recruit_search の配列の列）。駅は数えない
var matchColumns = []string{"employment_codes", "area_codes", "occupation_codes"}

type ranked struct {
	recruitID  int32
	cooc       int32
	matches    int32
	similarity float64
	score      float64 // おすすめの得点（related では、共起の次の並び替えに使う「条件の得点」）
}

// matchSQL は、候補（別名 candidate）と基準（別名 pivot）の、条件の一致の数を出す SQL の式。別名と列名は、このモジュールの固定の文字列だけ。
func matchSQL(candidate, pivot string) string {
	parts := make([]string, len(matchColumns))
	for i, col := range matchColumns {
		parts[i] = fmt.Sprintf("(SELECT count(*) FROM unnest(%s.%s) AS x WHERE x = ANY(%s.%s))", candidate, col, pivot, col)
	}
	return strings.Join(parts, " + ")
}

// pyRound9 は Python の round(x, 9)（正確な 2 進の値から、偶数丸め）。浮動小数点の端数で、同点の並びが ID 順にならないのを防ぐ。
func pyRound9(x float64) float64 {
	v, err := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 9, 64), 64)
	if err != nil {
		return x
	}
	return v
}

// conditionScore は、条件の一致の数に、埋め込みの類似度 × embeddingWeight を足したもの。
func conditionScore(matches int32, similarity float64) float64 {
	return pyRound9(float64(matches) + embeddingWeight*similarity)
}

// ---- 埋め込み ----

type embedding struct {
	model  string
	vector []float64
}

// pySum は Python 3.12 以降の sum()（float の補償つき加算。Neumaier）と同じ結果を返す。
func pySum(xs []float64) float64 {
	var f, c float64
	for _, x := range xs {
		t := f + x
		if math.Abs(f) >= math.Abs(x) {
			c += (f - t) + x
		} else {
			c += (x - t) + f
		}
		f = t
	}
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		f += c
	}
	return f
}

// cosine はコサイン類似度（0〜1）。モデルか次元が違う・長さが 0 のベクトルがある・負になるときは 0。
func cosine(a, b embedding) float64 {
	if a.model != b.model || len(a.vector) != len(b.vector) {
		return 0
	}
	sq := func(v []float64) float64 {
		xs := make([]float64, len(v))
		for i, x := range v {
			xs[i] = x * x
		}
		return math.Sqrt(pySum(xs))
	}
	na, nb := sq(a.vector), sq(b.vector)
	if na == 0 || nb == 0 {
		return 0
	}
	prod := make([]float64, len(a.vector))
	for i := range a.vector {
		prod[i] = a.vector[i] * b.vector[i]
	}
	value := pySum(prod) / (na * nb)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Min(1, math.Max(0, value))
}

// loadEmbeddings は、渡した求人のうち、埋め込みがあるものを 1 回の SQL で引く。ids が空なら SQL は流さない。
func (s *server) loadEmbeddings(ctx context.Context, ids []int32) (map[int32]embedding, error) {
	out := map[int32]embedding{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT recruit_id, model, vector FROM recruit_embeddings WHERE recruit_id = ANY($1::int[]) ORDER BY recruit_id", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int32
		var e embedding
		if err := rows.Scan(&id, &e.model, &e.vector); err != nil {
			return nil, err
		}
		out[id] = e
	}
	return out, rows.Err()
}

// similaritySums は、候補ごとに、基準の求人との類似度の合計。基準にも候補にも埋め込みがあるものだけが、結果に入る（入らない候補は 0）。
// 基準に埋め込みが 1 つも無ければ、候補の分は引かない（SQL は 1 回。ある場合は 2 回。求人の件数に比例しない）。
func (s *server) similaritySums(ctx context.Context, pivotIDs, candidateIDs []int32) (map[int32]float64, error) {
	sums := map[int32]float64{}
	pivots, err := s.loadEmbeddings(ctx, pivotIDs)
	if err != nil || len(pivots) == 0 {
		return sums, err
	}
	candidates, err := s.loadEmbeddings(ctx, candidateIDs)
	if err != nil {
		return nil, err
	}
	pivotIDsSorted := make([]int32, 0, len(pivots))
	for id := range pivots {
		pivotIDsSorted = append(pivotIDsSorted, id)
	}
	sort.Slice(pivotIDsSorted, func(i, j int) bool { return pivotIDsSorted[i] < pivotIDsSorted[j] })
	for cid, emb := range candidates {
		vals := make([]float64, 0, len(pivots))
		for _, pid := range pivotIDsSorted {
			vals = append(vals, cosine(pivots[pid], emb))
		}
		if total := pySum(vals); total > 0 {
			sums[cid] = total
		}
	}
	return sums, nil
}

// ---- 順位づけ ----

// rankRelated は、求人 recruitID に関連する、表示中の求人（自分は含めない）。
// 並び: (1) 同じ利用者が両方を閲覧した、その利用者の数（共起）の多い順、(2) 条件（雇用形態・エリア・職種）の一致の数
// （埋め込みがあれば、その類似度 × embeddingWeight を足したもの）の多い順、(3) ID の昇順。
// 共起が 0 でも、条件が 1 つも合わなくても、表示中なら入る（上の並びで後ろになるだけ）。
func (s *server) rankRelated(ctx context.Context, recruitID int32, limit int) ([]ranked, error) {
	rows, err := s.db.Query(ctx,
		"WITH co AS ("+
			"  SELECT o2.recruit_id, count(DISTINCT o1.user_id) AS n FROM recruit_events o1"+
			"  JOIN recruit_events o2 ON o2.user_id = o1.user_id AND o2.event_type = 'view' AND o2.recruit_id <> o1.recruit_id"+
			"  WHERE o1.recruit_id = $1 AND o1.event_type = 'view' GROUP BY o2.recruit_id"+
			") "+
			"SELECT c.id AS recruit_id, COALESCE(co.n, 0)::int AS cooc, ("+matchSQL("c", "t")+")::int AS matches "+
			"FROM recruit_search c JOIN recruit_search t ON t.id = $1 LEFT JOIN co ON co.recruit_id = c.id "+
			"WHERE c.is_displayed AND c.id <> $1", recruitID)
	if err != nil {
		return nil, err
	}
	type row struct{ id, cooc, matches int32 }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.cooc, &x.matches)
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int32, len(list))
	for i, x := range list {
		ids[i] = x.id
	}
	sims, err := s.similaritySums(ctx, []int32{recruitID}, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ranked, 0, len(list))
	for _, x := range list {
		sim := sims[x.id]
		out = append(out, ranked{x.id, x.cooc, x.matches, sim, conditionScore(x.matches, sim)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.cooc != b.cooc {
			return a.cooc > b.cooc
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.recruitID < b.recruitID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// rankRecommendations は、利用者へのおすすめ。その人がまだ見ていない・表示中の求人から選ぶ。閲覧の履歴が無ければ空。
// 得点 = 見た求人との共起の数の合計 × cooccurrenceWeight + 見た求人との条件の一致の数の合計（+ 埋め込みの類似度の合計 × embeddingWeight）。
// 得点 0 の求人は出さない。同点は ID の昇順。
// 共起の数 = 見た求人 v と候補 c を、どちらも見た「別の利用者」の数（利用者ごとに 1 と数える）。v ごとに数えて足す。
func (s *server) rankRecommendations(ctx context.Context, userID int32, limit int) ([]ranked, error) {
	vrows, err := s.db.Query(ctx, "SELECT DISTINCT recruit_id FROM recruit_events WHERE user_id = $1 AND event_type = 'view' ORDER BY recruit_id", userID)
	if err != nil {
		return nil, err
	}
	seen, err := pgx.CollectRows(vrows, pgx.RowTo[int32])
	if err != nil {
		return nil, err
	}
	if len(seen) == 0 {
		return []ranked{}, nil
	}
	rows, err := s.db.Query(ctx,
		"WITH co AS ("+
			"  SELECT p.cand_id, count(*) AS n FROM ("+
			"    SELECT DISTINCT s.v AS seen_id, o2.user_id, o2.recruit_id AS cand_id FROM unnest($1::int[]) AS s(v)"+
			"    JOIN recruit_events o1 ON o1.recruit_id = s.v AND o1.event_type = 'view'"+
			"    JOIN recruit_events o2 ON o2.user_id = o1.user_id AND o2.event_type = 'view' AND o2.recruit_id <> s.v"+
			"  ) p GROUP BY p.cand_id"+
			") "+
			"SELECT c.id AS recruit_id, COALESCE(co.n, 0)::int AS cooc, sum("+matchSQL("c", "v")+")::int AS matches "+
			"FROM recruit_search c JOIN recruit_search v ON v.id = ANY($1::int[]) LEFT JOIN co ON co.cand_id = c.id "+
			"WHERE c.is_displayed AND c.id <> ALL($1::int[]) GROUP BY c.id, co.n", seen)
	if err != nil {
		return nil, err
	}
	type row struct{ id, cooc, matches int32 }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.cooc, &x.matches)
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int32, len(list))
	for i, x := range list {
		ids[i] = x.id
	}
	sims, err := s.similaritySums(ctx, seen, ids)
	if err != nil {
		return nil, err
	}
	out := []ranked{}
	for _, x := range list {
		sim := sims[x.id]
		score := pyRound9(float64(x.cooc)*cooccurrenceWeight + conditionScore(x.matches, sim))
		if score > 0 {
			out = append(out, ranked{x.id, x.cooc, x.matches, sim, score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].recruitID < out[j].recruitID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// summaries は、求人の ID の並びのとおりに、一覧と同じ形（hero_image 込み）にする。SQL は、求人の件数によらず（空でなければ）2 回: 求人の本体と画像。
func (s *server) summaries(ctx context.Context, ids []int32) ([]recruitSummary, error) {
	out := []recruitSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		fmt.Sprintf("SELECT %s, %s FROM recruit_search s WHERE s.id = ANY($1::int[]) AND s.is_displayed", summaryCols, namesSQL), ids)
	if err != nil {
		return nil, err
	}
	byID := map[int32]recruitSummary{}
	for rows.Next() {
		item, err := scanSummary(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byID[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ordered := make([]int32, 0, len(ids))
	for _, id := range ids {
		if _, ok := byID[id]; ok { // 順位づけのあとで非表示になった求人は、落とす
			ordered = append(ordered, id)
		}
	}
	heroes, err := s.heroImages(ctx, ordered)
	if err != nil {
		return nil, err
	}
	for _, id := range ordered {
		item := byID[id]
		if h, ok := heroes[id]; ok {
			hero := h
			item.HeroImage = &hero
		}
		out = append(out, item)
	}
	return out, nil
}

// ---- API ----

// pathRecruitID は {id} を読む。int4 の範囲を外れる id は、どの求人でもない（ok=false）。
func pathRecruitID(r *http.Request) (id int32, inRange bool, e *httpError) {
	var v validation
	n := v.intParam([]any{"path", "recruit_id"}, r.PathValue("id"), nil, nil)
	if e := v.err(); e != nil {
		return 0, false, e
	}
	if !n.IsInt64() || n.Int64() < math.MinInt32 || n.Int64() > math.MaxInt32 {
		return 0, false, nil
	}
	return int32(n.Int64()), true, nil
}

// readLimit は ?limit=（1〜12。既定は def）。
func readLimit(r *http.Request, def int) (int, *httpError) {
	raw, ok := lastValue(r.URL.Query(), "limit")
	if !ok {
		return def, nil
	}
	var v validation
	n := v.intParam([]any{"query", "limit"}, raw, i64(1), i64(12))
	if e := v.err(); e != nil {
		return 0, e
	}
	return int(n.Int64()), nil
}

// recordView は閲覧を記録する。同じ利用者・同じ求人は、直近 dedupMinutes 分に 1 件あれば足さない（それでも 204）。表示中でない・無い求人は 404。
func (s *server) recordView(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	u, e := s.currentUser(ctx, r) // FastAPI は、依存（認証）を先に解く
	if e != nil {
		return e
	}
	id, inRange, e := pathRecruitID(r)
	if e != nil {
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
	// 求人の行を共有ロックして確かめる（このトランザクションのあいだ、求人が消えない）。recruit_search は集約のビューなので、同じ is_displayed を持つ recruits を見る
	var one int
	switch err := tx.QueryRow(ctx, "SELECT 1 FROM recruits WHERE id = $1 AND is_displayed FOR KEY SHARE", id).Scan(&one); err {
	case nil:
	case pgx.ErrNoRows:
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	default:
		return s.dbFailure(err)
	}
	// 同じ利用者・同じ求人の同時のリクエストが、どちらも「無い」と見て 2 件足さないよう、この組だけを直列にする（コミットまで）
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1::int, $2::int)", u.ID, id); err != nil {
		return s.dbFailure(err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO recruit_events (user_id, recruit_id, event_type) SELECT $1::int, $2::int, 'view' "+
			"WHERE NOT EXISTS (SELECT 1 FROM recruit_events WHERE user_id = $1 AND recruit_id = $2 AND event_type = 'view' "+
			"AND created_at > now() - make_interval(mins => $3::int))", u.ID, id, int32(dedupMinutes)); err != nil {
		return s.dbFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *server) related(w http.ResponseWriter, r *http.Request) *httpError {
	// 検査の誤りは、宣言の順（path → query）にまとめて返す
	var v validation
	n := v.intParam([]any{"path", "recruit_id"}, r.PathValue("id"), nil, nil)
	limit := 4
	if raw, ok := lastValue(r.URL.Query(), "limit"); ok {
		if m := v.intParam([]any{"query", "limit"}, raw, i64(1), i64(12)); m != nil {
			limit = int(m.Int64())
		}
	}
	if e := v.err(); e != nil {
		return e
	}
	if !n.IsInt64() || n.Int64() < math.MinInt32 || n.Int64() > math.MaxInt32 {
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	}
	id := int32(n.Int64())
	ctx, cancel := dbContext(r)
	defer cancel()
	var one int
	switch err := s.db.QueryRow(ctx, "SELECT 1 FROM recruits WHERE id = $1 AND is_displayed", id).Scan(&one); err {
	case nil:
	case pgx.ErrNoRows:
		return newHTTPError(http.StatusNotFound, detailNoRecruit)
	default:
		return s.dbFailure(err)
	}
	rk, err := s.rankRelated(ctx, id, limit)
	if err != nil {
		return s.dbFailure(err)
	}
	ids := make([]int32, len(rk))
	for i, x := range rk {
		ids[i] = x.recruitID
	}
	items, err := s.summaries(ctx, ids)
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

func (s *server) recommendations(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	u, e := s.currentUser(ctx, r)
	if e != nil {
		return e
	}
	limit, e := readLimit(r, 6)
	if e != nil {
		return e
	}
	rk, err := s.rankRecommendations(ctx, u.ID, limit)
	if err != nil {
		return s.dbFailure(err)
	}
	ids := make([]int32, len(rk))
	for i, x := range rk {
		ids[i] = x.recruitID
	}
	items, err := s.summaries(ctx, ids)
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

// setEmbedding は求人の埋め込みを入れる（あれば置き換える）。トランザクションは呼ぶ側が決める。値を入れる CLI・API は作っていない
// （埋め込みを作るには実 API が要る。入れるときは、回数と費用を示して承認を得てから、別の道具で行う）。
// vector は、1 つ以上の有限の数。model は、1〜200 文字のモデルの名前（モデルが違うベクトルどうしは比べない）。求人が無ければ、外部キーのエラーになる。
func setEmbedding(ctx context.Context, tx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, recruitID int32, model string, vector []float64) error {
	if n := utf8.RuneCountInString(model); n < 1 || n > 200 {
		return errors.New("model は 1〜200 文字の文字列にしてください")
	}
	if len(vector) == 0 {
		return errors.New("vector は、1 つ以上の有限の数の並びにしてください")
	}
	for _, x := range vector {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return errors.New("vector は、1 つ以上の有限の数の並びにしてください")
		}
	}
	_, err := tx.Exec(ctx,
		"INSERT INTO recruit_embeddings (recruit_id, model, vector) VALUES ($1::int, $2, $3::float8[]) "+
			"ON CONFLICT (recruit_id) DO UPDATE SET model = EXCLUDED.model, vector = EXCLUDED.vector, updated_at = now()",
		recruitID, model, vector)
	return err
}
