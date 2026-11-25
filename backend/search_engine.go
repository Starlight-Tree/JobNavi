package main

// 検索エンジン（Meilisearch）。Postgres が正で、ここは写し（索引）。backend/app/search_engine.py の移植（検索の部分。索引づくりは search_index.go）。
// 使うのは、キーワード（q）の「候補の id（関連度の順）」を引くことだけ。表示中か・条件に合うか・権限は、いつも SQL が判定する。
// 届かない・期待した応答でないときは errEngineUnavailable。呼び出し側は Postgres の LIKE に戻る。エラーの文は固定（URL・鍵を含めない）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	engineIndexUID    = "recruits"
	engineSearchLimit = 1000 // 受け取った数がこれに届いたら、取りこぼしの恐れがあるので使わない
	engineLocale      = "jpn"
	engineCooldown    = 10 * time.Second // 届かなかったあと、この間は聞かずに Postgres で探す
	engineMaxParallel = 4                // 同時に待つ数の上限。超えた分は待たずに Postgres で探す
	engineMaxBody     = 16 << 20
)

// 索引に入れる列。順番が、関連度の重み。recruit_search_text（0009）の対象の列と同じ
var engineSearchable = []string{"category", "corporate", "profile", "place", "comment1", "comment2", "comment3"}

// engineError は、検索エンジンに届かない、または期待した応答でない。呼び出し側は Postgres の検索に戻る。status は HTTP の状態（届かなければ 0）。
type engineError struct {
	msg    string
	status int
}

func (e *engineError) Error() string { return e.msg }

func engErr(msg string) error { return &engineError{msg: msg} }

var (
	errEngineUnavailable = engErr("検索エンジンを使えません")
	errResultTooLarge    = engErr("検索結果が多く、全件を取れません") // 止まっているわけではないので、聞かない時間は作らない
)

type searchEngine struct {
	cfg    meiliConfig
	client *http.Client
	logf   func(string, ...any)
	slots  chan struct{}

	mu        sync.Mutex
	downUntil time.Time
}

func newSearchEngine(cfg meiliConfig, logf func(string, ...any)) *searchEngine {
	return &searchEngine{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// 3xx は辿らない（Authorization を別のホストへ送らないため）。3xx は届かないのと同じ扱い
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		logf:  logf,
		slots: make(chan struct{}, engineMaxParallel),
	}
}

func (e *searchEngine) resetCooldown() {
	e.mu.Lock()
	e.downUntil = time.Time{}
	e.mu.Unlock()
}

// call は 1 回の呼び出し。2xx 以外・届かない・読めないは errEngineUnavailable（statusOut は HTTP の状態。届かなければ 0）。
func (e *searchEngine) call(ctx context.Context, method, path string, body any) (any, int, error) {
	var reader io.Reader
	if body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return nil, 0, errEngineUnavailable
		}
		reader = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, e.cfg.URL+path, reader)
	if err != nil {
		return nil, 0, errEngineUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.Key)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, engErr("検索エンジンに届きません")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, engineMaxBody))
	if err != nil {
		return nil, 0, engErr("検索エンジンに届きません")
	}
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &engineError{msg: fmt.Sprintf("検索エンジンが %d を返しました", resp.StatusCode), status: resp.StatusCode}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, resp.StatusCode, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, resp.StatusCode, engErr("検索エンジンの応答を読めません")
	}
	return out, resp.StatusCode, nil
}

// usableTerms は、畳んだ語を、そのままエンジンに渡してよいか。Meilisearch の検索構文（語頭の - は除外、" は句）を含む語・空の語・
// 文字も数字も無い語は渡さない（Postgres の LIKE では、ただの文字なので、経路によって意味が変わる。Postgres で探す）。
func usableTerms(folded []string) bool {
	for _, t := range folded {
		if t == "" || strings.HasPrefix(t, "-") || strings.Contains(t, `"`) {
			return false
		}
		word := false
		for _, r := range t {
			if r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) {
				word = true
				break
			}
		}
		if !word {
			return false
		}
	}
	return true
}

// searchIDs は、畳んだ検索語（空白で区切る。すべての語を含む求人だけ）に合う求人の id を、関連度の順で返す。
// 届かない・エラー・応答が不正なときは、engineCooldown の間、聞かずに errEngineUnavailable にする。
func (e *searchEngine) searchIDs(ctx context.Context, query string) ([]int64, error) {
	e.mu.Lock()
	down := time.Now().Before(e.downUntil)
	e.mu.Unlock()
	if down {
		return nil, engErr("検索エンジンを一時的に使っていません")
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return nil, engErr("検索エンジンが混み合っています") // 止まっているとは限らないので、聞かない時間は作らない
	}
	ids, err := e.search(ctx, query)
	if err != nil && !errors.Is(err, errResultTooLarge) {
		e.mu.Lock()
		e.downUntil = time.Now().Add(engineCooldown)
		e.mu.Unlock()
	}
	return ids, err
}

func (e *searchEngine) search(ctx context.Context, query string) ([]int64, error) {
	out, _, err := e.call(ctx, http.MethodPost, "/indexes/"+engineIndexUID+"/search", map[string]any{
		"q": query, "limit": engineSearchLimit, "matchingStrategy": "all", "attributesToRetrieve": []string{"id"}, "locales": []string{engineLocale},
	})
	if err != nil {
		return nil, err
	}
	obj, ok := out.(map[string]any)
	if !ok {
		return nil, engErr("検索エンジンの応答を読めません")
	}
	hits, ok := obj["hits"].([]any)
	if !ok {
		return nil, engErr("検索エンジンの応答を読めません")
	}
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		m, ok := h.(map[string]any)
		if !ok {
			return nil, engErr("検索エンジンの応答を読めません")
		}
		num, ok := m["id"].(json.Number)
		if !ok {
			return nil, engErr("検索エンジンの応答を読めません")
		}
		id, err := num.Int64()
		if err != nil {
			return nil, engErr("検索エンジンの応答を読めません")
		}
		ids = append(ids, id)
	}
	if len(hits) >= engineSearchLimit {
		return nil, errResultTooLarge
	}
	return ids, nil
}
