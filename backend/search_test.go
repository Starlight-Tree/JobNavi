package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMeili は、Meilisearch の最小の偽物。検索（POST /indexes/recruits/search）に、決めた id を返し、索引づくりの呼び出し（タスク・設定・文書・入れ替え）を記録する。
type fakeMeili struct {
	mu       sync.Mutex
	hits     []any // 検索が返す hits
	status   int   // 0 以外なら、検索にその状態を返す
	searches []map[string]any
	auth     []string
	calls    []string
	indexes  map[string]bool
	docs     map[string][]map[string]any
	settings map[string]any
	task     int
}

func newFakeMeili(t *testing.T) (*fakeMeili, *httptest.Server) {
	f := &fakeMeili{indexes: map[string]bool{}, docs: map[string][]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeMeili) ids(ids ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits = nil
	for _, id := range ids {
		f.hits = append(f.hits, map[string]any{"id": id})
	}
}

func (f *fakeMeili) searchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.searches)
}

func (f *fakeMeili) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	var body any
	_ = json.Unmarshal(raw, &body)
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	task := func() { f.task++; reply(http.StatusAccepted, map[string]any{"taskUid": f.task}) }
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/indexes/recruits/search":
		f.searches = append(f.searches, body.(map[string]any))
		if f.status != 0 {
			reply(f.status, map[string]any{"message": "x"})
			return
		}
		reply(http.StatusOK, map[string]any{"hits": f.hits})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/tasks/"):
		reply(http.StatusOK, map[string]any{"status": "succeeded"})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/indexes/") && strings.Count(path, "/") == 2:
		if f.indexes[strings.TrimPrefix(path, "/indexes/")] {
			reply(http.StatusOK, map[string]any{"uid": strings.TrimPrefix(path, "/indexes/")})
		} else {
			reply(http.StatusNotFound, map[string]any{"code": "index_not_found"})
		}
	case r.Method == http.MethodPost && path == "/indexes":
		f.indexes[body.(map[string]any)["uid"].(string)] = true
		task()
	case r.Method == http.MethodPatch && strings.HasSuffix(path, "/settings"):
		f.settings = body.(map[string]any)
		task()
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/documents"):
		uid := strings.Split(path, "/")[2]
		for _, d := range body.([]any) {
			f.docs[uid] = append(f.docs[uid], d.(map[string]any))
		}
		task()
	case r.Method == http.MethodPost && path == "/swap-indexes":
		pair := body.([]any)[0].(map[string]any)["indexes"].([]any)
		a, b := pair[0].(string), pair[1].(string)
		f.docs[a], f.docs[b] = f.docs[b], f.docs[a]
		task()
	case r.Method == http.MethodPatch && strings.Count(path, "/") == 2: // 改名
		old := strings.TrimPrefix(path, "/indexes/")
		f.docs[body.(map[string]any)["uid"].(string)] = f.docs[old]
		delete(f.docs, old)
		delete(f.indexes, old)
		f.indexes[body.(map[string]any)["uid"].(string)] = true
		task()
	case r.Method == http.MethodDelete:
		uid := strings.TrimPrefix(path, "/indexes/")
		delete(f.indexes, uid)
		delete(f.docs, uid)
		task()
	case r.Method == http.MethodGet && path == "/health":
		reply(http.StatusOK, map[string]any{"status": "available"})
	case r.Method == http.MethodGet && path == "/indexes/recruits/stats":
		reply(http.StatusOK, map[string]any{"numberOfDocuments": len(f.docs["recruits"])})
	default:
		reply(http.StatusNotFound, map[string]any{"message": "unexpected " + r.Method + " " + path})
	}
}

func meiliApp(t *testing.T, url string) *testApp {
	return newApp(t, func(m map[string]string) {
		m["SEARCH_ENGINE"] = "meilisearch"
		m["MEILI_URL"] = url
		m["MEILI_API_KEY"] = "test-key"
	})
}

type pageBody struct {
	Total int64            `json:"total"`
	Items []recruitSummary `json:"items"`
}

func idsOf(p pageBody) []int32 {
	out := []int32{}
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

func TestKeywordSearchUsesEngineCandidates(t *testing.T) {
	fake, srv := newFakeMeili(t)
	app := meiliApp(t, srv.URL)
	hidden := app.queryInt("SELECT id FROM recruits WHERE NOT is_displayed LIMIT 1")
	fake.hits = []any{map[string]any{"id": 5}, map[string]any{"id": 3}, map[string]any{"id": hidden}, map[string]any{"id": 99999}, map[string]any{"id": 9}}

	var page pageBody
	r := app.get("/api/recruits?q="+urlEscape("おんしつ ｶﾞｰﾃﾞﾝ"), nil)
	r.JSON(t, &page)
	// 関連度の順（候補の並び）。非表示の求人・存在しない求人は、SQL が落とす
	if got := idsOf(page); r.Status != 200 || fmt.Sprint(got) != "[5 3 9]" || page.Total != 3 {
		t.Fatalf("候補の順: %d %v total=%d", r.Status, got, page.Total)
	}
	req := fake.searches[0]
	if req["q"] != "オンシツ ガーデン" || req["matchingStrategy"] != "all" || req["limit"].(float64) != 1000 || fmt.Sprint(req["locales"]) != "[jpn]" {
		t.Errorf("エンジンへの問い合わせ（畳んだ語を渡す）: %v", req)
	}
	if fake.auth[0] != "Bearer test-key" {
		t.Errorf("認証ヘッダー: %q", fake.auth[0])
	}
	// 条件との AND・ページ分け・件数
	app.get("/api/recruits?per_page=2&page=2&q=x", nil).JSON(t, &page)
	if fmt.Sprint(idsOf(page)) != "[9]" || page.Total != 3 {
		t.Errorf("ページ 2: %v total=%d", idsOf(page), page.Total)
	}
	var f facets
	app.get("/api/facets?q=x", nil).JSON(t, &f)
	if f.Total != 3 {
		t.Errorf("facets の total: %d", f.Total)
	}
	// 候補が空なら、0 件（Postgres には戻らない）
	fake.ids()
	app.get("/api/recruits?q=x", nil).JSON(t, &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Errorf("候補が空: %d", page.Total)
	}
	// 検索語が無い・使えない語のときは、エンジンに聞かない
	before := fake.searchCount()
	for _, q := range []string{"", "%20", "-x", urlEscape(`"x"`), urlEscape("！"), urlEscape("・")} {
		if got := app.get("/api/recruits?q="+q, nil).Status; got != 200 {
			t.Errorf("q=%s: %d", q, got)
		}
	}
	if fake.searchCount() != before {
		t.Errorf("使えない語でエンジンを呼びました: %d 回", fake.searchCount()-before)
	}
	if got := app.get("/api/recruits", nil); got.Status != 200 || fake.searchCount() != before {
		t.Error("q が無いときは、エンジンに聞かない")
	}
}

func TestKeywordSearchFallsBackToPostgres(t *testing.T) {
	fake, srv := newFakeMeili(t)
	app := meiliApp(t, srv.URL)
	pg := newApp(t) // 同じ DB の内容を、Postgres の検索で
	var want pageBody
	pg.get("/api/recruits?q="+urlEscape("温室"), nil).JSON(t, &want)
	if want.Total == 0 {
		t.Fatal("基準の検索が 0 件です")
	}
	var got pageBody
	// 結果が多すぎる（1000 件に届いた）: 取りこぼしの恐れがあるので、Postgres で探す。止まっているわけではないので、次も聞く
	fake.hits = make([]any, 1000)
	for i := range fake.hits {
		fake.hits[i] = map[string]any{"id": i + 1}
	}
	app.get("/api/recruits?q="+urlEscape("温室"), nil).JSON(t, &got)
	if got.Total != want.Total || fmt.Sprint(idsOf(got)) != fmt.Sprint(idsOf(want)) {
		t.Errorf("too large: %v（期待 %v）", idsOf(got), idsOf(want))
	}
	app.get("/api/recruits?q="+urlEscape("温室"), nil)
	if fake.searchCount() != 2 {
		t.Errorf("結果が多すぎるときは、聞かない時間を作らない: %d 回", fake.searchCount())
	}
	// エラー・応答が不正: Postgres で探し、しばらく聞かない
	for name, setup := range map[string]func(){
		"500":        func() { fake.status = 500 },
		"not a list": func() { fake.status = 0; fake.hits = nil },
	} {
		app.srv.engine.resetCooldown()
		setup()
		if name == "not a list" {
			fake.hits = []any{"x"}
		}
		n := fake.searchCount()
		app.get("/api/recruits?q="+urlEscape("温室"), nil).JSON(t, &got)
		if got.Total != want.Total {
			t.Errorf("%s: 件数 %d（期待 %d）", name, got.Total, want.Total)
		}
		app.get("/api/recruits?q="+urlEscape("温室"), nil)
		if fake.searchCount() != n+1 {
			t.Errorf("%s: 落ちたあとは、聞かずに Postgres で探すはず（呼び出し %d 回）", name, fake.searchCount()-n)
		}
	}
	// 届かない
	srv.Close()
	app.srv.engine.resetCooldown()
	app.get("/api/recruits?q="+urlEscape("温室"), nil).JSON(t, &got)
	if got.Total != want.Total {
		t.Errorf("届かない: %d", got.Total)
	}
	// クールダウンが明ければ、また聞く
	app.srv.engine.mu.Lock()
	app.srv.engine.downUntil = time.Now().Add(-time.Second)
	app.srv.engine.mu.Unlock()
	if app.srv.engine.searchIDsDown() {
		t.Error("クールダウンが明けたのに、聞かない状態のまま")
	}
}

func (e *searchEngine) searchIDsDown() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Now().Before(e.downUntil)
}

func TestReindexBuildsFromDatabase(t *testing.T) {
	fake, srv := newFakeMeili(t)
	app := meiliApp(t, srv.URL)
	ctx := context.Background()
	hiddenCount := app.queryInt("SELECT count(*) FROM recruits WHERE NOT is_displayed")
	shown := app.queryInt("SELECT count(*) FROM recruits WHERE is_displayed")
	if hiddenCount == 0 {
		t.Fatal("非表示の求人が無い")
	}
	var lines []string
	n, err := app.srv.engine.reindex(ctx, app.pool, func(s string) { lines = append(lines, s) })
	if err != nil || int64(n) != shown || len(lines) != 1 || lines[0] != fmt.Sprintf("%d 件を索引に入れました", shown) {
		t.Fatalf("reindex: %d %v %v", n, err, lines)
	}
	if len(fake.docs["recruits"]) != int(shown) || fake.indexes["recruits_next"] || !fake.indexes["recruits"] {
		t.Errorf("索引が無いときは、別名の索引に作って、使う名前に改名するはず: %v %d", fake.indexes, len(fake.docs["recruits"]))
	}
	doc := fake.docs["recruits"][0]
	for _, c := range engineSearchable {
		if _, ok := doc[c]; !ok {
			t.Errorf("列 %s が文書にありません", c)
		}
	}
	// 文は search_fold を通した形（ひらがな → カタカナ・半角カナ → 全角・小文字）
	var folded string
	app.pool.QueryRow(ctx, "SELECT search_fold(category) FROM recruits WHERE id = 1").Scan(&folded)
	if doc["id"].(float64) != 1 || doc["category"] != folded {
		t.Errorf("1 件目: %v（期待 category=%q）", doc["category"], folded)
	}
	settings := fake.settings
	if fmt.Sprint(settings["searchableAttributes"]) != fmt.Sprint(engineSearchable) || fmt.Sprint(settings["displayedAttributes"]) != "[id]" {
		t.Errorf("設定: %v", settings)
	}
	// 2 回目: すでに索引がある → 入れ替え（swap）。入れ替えのあと、別名の索引を消す
	fake.calls = nil
	if _, err := app.srv.engine.reindex(ctx, app.pool, func(string) {}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.calls, "\n")
	if !strings.Contains(joined, "POST /swap-indexes") || !strings.Contains(joined, "DELETE /indexes/recruits_next") || fake.indexes["recruits_next"] {
		t.Errorf("2 回目の呼び出し:\n%s", joined)
	}
	if len(fake.docs["recruits"]) != int(shown) {
		t.Errorf("入れ替え後の文書: %d", len(fake.docs["recruits"]))
	}
	exists, count, err := app.srv.engine.status(ctx)
	if err != nil || !exists || count != shown {
		t.Errorf("status: %v %d %v", exists, count, err)
	}
	// 前回の途中の残りは、先に消す
	fake.indexes["recruits_next"] = true
	fake.calls = nil
	if _, err := app.srv.engine.reindex(ctx, app.pool, func(string) {}); err != nil || !strings.Contains(strings.Join(fake.calls, "\n"), "DELETE /indexes/recruits_next") {
		t.Errorf("前回の残り: %v", err)
	}
}
