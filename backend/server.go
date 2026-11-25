package main

// HTTP の入口。経路・応答の形・エラーの文は、Python（FastAPI）の API と同じにする（backend/app/main.py の移植）。
//   - 応答の JSON は compact（非 ASCII もそのまま）。項目の順は pydantic のモデルと同じ
//   - 404 は {"detail":"Not Found"}、405 は {"detail":"Method Not Allowed"} + Allow、500 は本文 "Internal Server Error"（text/plain）
//   - 422 は pydantic v2 の形（type / loc / msg / input / ctx）。複数の誤りは、宣言の順にまとめて返す
//   - エラー本文は固定の文。例外の文字列・パス・接続先を応答にもログにも出さない

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dbTimeout  = 15 * time.Second
	corsMaxAge = "600" // Starlette の CORSMiddleware の既定（max_age=600）
)

type issue struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx,omitempty"`
}

type httpError struct {
	status int
	detail string
	header map[string]string
	issues []issue // 422 のとき
	cause  error   // 内部の原因（応答には出さない）
}

func (e *httpError) Error() string { return e.detail }

func newHTTPError(status int, detail string) *httpError {
	return &httpError{status: status, detail: detail}
}

// internalError は FastAPI の 500（本文は text/plain の "Internal Server Error"。例外の中身は返さない）。
var internalError = &httpError{status: http.StatusInternalServerError, detail: "Internal Server Error"}

func writeJSON(w http.ResponseWriter, status int, body any, header map[string]string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	w.Header().Set("Content-Type", "application/json")
	for k, v := range header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

func writeError(w http.ResponseWriter, e *httpError) {
	switch {
	case e.issues != nil:
		writeJSON(w, e.status, map[string]any{"detail": e.issues}, e.header)
	case e == internalError || e.status == http.StatusInternalServerError:
		// 500 は CORS の外側（ServerErrorMiddleware）で作られるので、Vary も Allow-Origin も付かない
		w.Header().Del("Vary")
		w.Header().Del("Access-Control-Allow-Origin")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	default:
		writeJSON(w, e.status, map[string]string{"detail": e.detail}, e.header)
	}
}

// ---- 422（pydantic v2 の整数・文字列の検査）----

type validation struct{ issues []issue }

func (v *validation) err() *httpError {
	if len(v.issues) == 0 {
		return nil
	}
	return &httpError{status: http.StatusUnprocessableEntity, detail: "Unprocessable Entity", issues: v.issues}
}

// parsePyInt は pydantic v2 の int（lax）の読み方と同じ: 前後の空白は無視、符号は +/-、数字は 0-9 だけ（全角は不可）、
// 桁の間の _ は可（1_000）、小数点以下がすべて 0 なら可（1.0）、指数・16 進は不可。
func parsePyInt(raw string) (*big.Int, bool) {
	text := strings.TrimFunc(raw, unicode.IsSpace)
	if text == "" {
		return nil, false
	}
	negative := false
	if text[0] == '+' || text[0] == '-' {
		negative = text[0] == '-'
		text = text[1:]
	}
	intPart, frac, hasFrac := strings.Cut(text, ".")
	if !digitsWithUnderscores(intPart) {
		return nil, false
	}
	if hasFrac && (frac == "" || strings.Trim(frac, "0") != "") {
		return nil, false
	}
	n, ok := new(big.Int).SetString(strings.ReplaceAll(intPart, "_", ""), 10)
	if !ok {
		return nil, false
	}
	if negative {
		n.Neg(n)
	}
	return n, true
}

func digitsWithUnderscores(s string) bool {
	if s == "" {
		return false
	}
	previousUnderscore := true // 先頭の _ を拒否する
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			previousUnderscore = false
		case r == '_' && !previousUnderscore:
			previousUnderscore = true
		default:
			return false
		}
	}
	return !previousUnderscore
}

// intParam は整数を読む。範囲（ge・le。nil なら無制限）を外れたとき・整数でないときは、誤りを足して nil を返す。
func (v *validation) intParam(loc []any, raw string, ge, le *int64) *big.Int {
	n, ok := parsePyInt(raw)
	if !ok {
		v.issues = append(v.issues, issue{Type: "int_parsing", Loc: loc, Msg: "Input should be a valid integer, unable to parse string as an integer", Input: raw})
		return nil
	}
	if ge != nil && n.Cmp(big.NewInt(*ge)) < 0 {
		v.issues = append(v.issues, issue{Type: "greater_than_equal", Loc: loc, Msg: fmt.Sprintf("Input should be greater than or equal to %d", *ge), Input: raw, Ctx: map[string]any{"ge": *ge}})
		return nil
	}
	if le != nil && n.Cmp(big.NewInt(*le)) > 0 {
		v.issues = append(v.issues, issue{Type: "less_than_equal", Loc: loc, Msg: fmt.Sprintf("Input should be less than or equal to %d", *le), Input: raw, Ctx: map[string]any{"le": *le}})
		return nil
	}
	return n
}

func i64(n int64) *int64 { return &n }

// ---- CORS（Starlette の CORSMiddleware と同じ: 許可したオリジンにだけ付ける。* も Allow-Credentials も使わない）----

func (s *server) corsAllowed(origin string) bool {
	for _, o := range s.cfg.CORSOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

var corsMethods = "GET, POST, PUT, PATCH, DELETE"

func (s *server) applyCORS(w http.ResponseWriter, r *http.Request) (preflightHandled bool) {
	w.Header().Add("Vary", "Origin") // Starlette は、Origin の有無によらず付ける
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	allowed := s.corsAllowed(origin)
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	if allowed {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if !preflight {
		return false
	}
	// Starlette の preflight_response と同じ: 許可しない origin・method は 400（理由を並べた本文）。ヘッダーは、許可の有無によらず付ける
	h := w.Header()
	h.Set("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers, Access-Control-Request-Private-Network")
	h.Set("Access-Control-Allow-Methods", corsMethods)
	h.Set("Access-Control-Max-Age", corsMaxAge)
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
		h.Set("Access-Control-Allow-Headers", requested)
	}
	var failures []string
	if !allowed {
		failures = append(failures, "origin")
	}
	if !strings.Contains(", "+corsMethods+",", ", "+r.Header.Get("Access-Control-Request-Method")+",") {
		failures = append(failures, "method")
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	if len(failures) > 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Disallowed CORS " + strings.Join(failures, ", ")))
		return true
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
	return true
}

// ---- 経路 ----

type handlerFunc func(w http.ResponseWriter, r *http.Request) *httpError

type server struct {
	cfg    config
	db     *pgxpool.Pool
	engine *searchEngine // SEARCH_ENGINE=meilisearch のときだけ
	jwt    *jwtVerifier  // AUTH_BASE_URL と AUTH_JWKS_URL があるときだけ
	store  storage
	// メールが無いときも同じだけ時間をかけるための、ダミーのハッシュ（起動時に 1 つ作る）
	dummyHash string
	logf      func(string, ...any)
	mux       *http.ServeMux
}

func newServer(cfg config, db *pgxpool.Pool, logf func(string, ...any)) (*server, error) {
	st, err := newStorage(cfg)
	if err != nil {
		return nil, err
	}
	s := &server{cfg: cfg, db: db, logf: logf, mux: http.NewServeMux(), store: st, dummyHash: hashPassword("dummy-password-for-timing")}
	if cfg.Meili != nil {
		s.engine = newSearchEngine(*cfg.Meili, logf)
	}
	if cfg.Auth != nil {
		s.jwt = newJWTVerifier(*cfg.Auth, logf)
	}
	s.route("/health", on("GET", s.health))
	s.route("/api/master", on("GET", s.master))
	s.route("/api/facets", on("GET", s.facets))
	s.route("/api/recruits", on("GET", s.searchRecruits))
	s.route("/api/recruits/{id}", on("GET", s.recruitDetail))
	s.route("/api/recruits/{id}/view", on("POST", s.recordView))
	s.route("/api/recruits/{id}/related", on("GET", s.related))
	s.route("/api/recommendations", on("GET", s.recommendations))
	s.route("/api/auth/me", on("GET", s.me), on("PATCH", s.updateName))

	// 園
	s.route("/api/garden/auth/login", on("POST", s.gardenLogin))
	s.route("/api/garden/auth/me", on("GET", s.gardenMe))
	s.route("/api/garden/auth/logout", on("POST", s.gardenLogout))
	s.route("/api/garden/summary", on("GET", s.gardenSummary))
	s.route("/api/garden/recruits", on("GET", s.gardenRecruits))
	s.route("/api/garden/recruits/{id}/images", on("GET", s.listRecruitImages), on("POST", s.uploadRecruitImage))
	s.route("/api/garden/recruits/{id}/images/order", on("PUT", s.reorderRecruitImages), on("DELETE", s.removeImageNamed("order")))
	s.route("/api/garden/recruits/{id}/images/hero", on("PUT", s.setHeroImage), on("DELETE", s.removeImageNamed("hero")))
	s.route("/api/garden/recruits/{id}/images/{image_id}", on("DELETE", s.removeRecruitImage))
	s.route("/api/garden/profile-images", on("GET", s.listProfileImages), on("POST", s.uploadProfileImage))
	s.route("/api/garden/profile-images/{image_id}", on("DELETE", s.removeProfileImage))
	return s, nil
}

// methodHandler は、パスの上のメソッドごとの処理。
type methodHandler struct {
	method string
	h      handlerFunc
}

func on(method string, h handlerFunc) methodHandler { return methodHandler{method, h} }

// route は、パスにメソッドごとの処理を結ぶ。結んでいないメソッドは 405（FastAPI と同じ。HEAD も 405）。
// Allow は、最初に結んだメソッドだけ（Starlette は、同じパスの最初の経路の methods を返す）。
func (s *server) route(path string, handlers ...methodHandler) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		for _, mh := range handlers {
			if mh.method == r.Method {
				if e := mh.h(w, r); e != nil {
					writeError(w, e)
				}
				return
			}
		}
		e := newHTTPError(http.StatusMethodNotAllowed, "Method Not Allowed")
		e.header = map[string]string{"Allow": handlers[0].method}
		writeError(w, e)
	})
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.applyCORS(w, r) {
		return
	}
	// ローカル保存のときだけ、画像を配る（S3 / R2 では、公開 URL の配信元が配る）。{key:path} と同じく、パスを整えずに受ける
	if s.cfg.StorageBackend == "local" && strings.HasPrefix(r.URL.Path, "/media/") {
		if r.Method != http.MethodGet {
			e := newHTTPError(http.StatusMethodNotAllowed, "Method Not Allowed")
			e.header = map[string]string{"Allow": "GET"}
			writeError(w, e)
			return
		}
		if e := s.media(w, r); e != nil {
			writeError(w, e)
		}
		return
	}
	if _, pattern := s.mux.Handler(r); pattern == "" {
		// 末尾の / だけが違うときは 307（Starlette の redirect_slashes）
		if p := r.URL.Path; len(p) > 1 && strings.HasSuffix(p, "/") {
			probe := r.Clone(r.Context())
			probe.URL.Path = strings.TrimRight(p, "/")
			if _, trimmed := s.mux.Handler(probe); trimmed != "" {
				target := *r.URL
				target.Path = probe.URL.Path
				target.RawPath = ""
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				target.Scheme, target.Host = scheme, r.Host
				w.Header().Set("Location", target.String())
				w.WriteHeader(http.StatusTemporaryRedirect)
				return
			}
		}
		writeError(w, newHTTPError(http.StatusNotFound, "Not Found"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) *httpError {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}, nil)
	return nil
}

// dbContext は、DB の問い合わせの時間の上限をつけた文脈。
func dbContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), dbTimeout)
}
