package main

// Python（FastAPI）の API が、同じ手順で作った DB に返した応答（testdata/golden_read.json）を、Go の API に再生して、一字一句そろっているかを確かめる。
// 読み取り系の経路（/health・マスタ・条件ごとの件数・検索・詳細）と、404・405・422・CORS の形が対象。uuid は <uuid> に直して比べる。
// 作り直し方: 旧 Python の API（`git show 4dc1c0f:backend/…` で取り出せる）を、同じ手順で作った DB に向けて動かし、同じ要求の応答を記録する。記録の道具は削除したので書き直す。応答の形を意図して変えたときは、golden を作り直す。

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

type goldenCase struct {
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Headers         map[string]string `json:"headers"`
	Status          int               `json:"status"`
	ResponseHeaders map[string]string `json:"response_headers"`
	Body            string            `json:"body"`
}

var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestGoldenReadAPI(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden_read.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	app := newApp(t)
	compared := []string{"content-type", "allow", "access-control-allow-origin", "access-control-allow-methods", "access-control-allow-headers", "access-control-max-age", "vary"}
	for _, c := range cases {
		got := app.do(c.Method, c.Path, c.Headers, nil)
		if got.Status != c.Status {
			t.Errorf("%s %s: 状態 %d（期待 %d）\n%s", c.Method, c.Path, got.Status, c.Status, truncate(got.Body, 300))
			continue
		}
		// HEAD は、本文を返さない（実サーバーは捨てる。httptest の記録は捨てない）
		if body := uuidPattern.ReplaceAllString(got.Body, "<uuid>"); c.Method != http.MethodHead && body != c.Body {
			t.Errorf("%s %s: 本文が違います\n got: %s\nwant: %s", c.Method, c.Path, truncate(body, 400), truncate(c.Body, 400))
		}
		for _, h := range compared {
			want, wantOK := c.ResponseHeaders[h]
			have := got.Header.Get(h)
			if wantOK && have != want || !wantOK && have != "" {
				t.Errorf("%s %s: ヘッダー %s = %q（期待 %q）", c.Method, c.Path, h, have, want)
			}
		}
		if want, ok := c.ResponseHeaders["location"]; ok {
			if have := got.Header.Get("Location"); !strings.HasSuffix(have, "/"+want) {
				t.Errorf("%s %s: Location = %q（期待 …/%s）", c.Method, c.Path, have, want)
			}
		}
	}
	if len(cases) < 200 {
		t.Errorf("golden が少なすぎます: %d", len(cases))
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
