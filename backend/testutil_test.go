package main

// テストの土台。DB を使うテストは TEST_DATABASE_URL（Postgres 17。例: postgresql://postgres:postgres@127.0.0.1:5432/postgres）が要る。
// 無いと、DB のテストはスキップされる（スキップのまま通っても、検証したことにならない。CI はスキップ 0 を確かめる）。
//
// 作り方（Python の conftest.py と同じ順）: テンプレートの DB を 1 つ作る（db/schema.sql と seed_codes.sql → migrate → seed --extend → seed-images）。
// テストごとに、そのテンプレートから複製した DB を使う（書き込みのあるテストが、互いに影響しない）。

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const templateDB = "jobnavi_go_template"

var (
	adminURL    = os.Getenv("TEST_DATABASE_URL")
	templateErr error
	templateMu  sync.Once
	mediaSource = filepath.Join("..", "frontend", "public", "images", "generated", "recruits")
)

func testEnvMap(m map[string]string) env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func withDB(rawURL, name string) string {
	u, _ := url.Parse(rawURL)
	u.Path = "/" + name
	return u.String()
}

func execSimple(ctx context.Context, conn *pgx.Conn, sql string) error {
	_, err := conn.PgConn().Exec(ctx, sql).ReadAll()
	return err
}

// buildTemplate はテンプレートの DB を作る（1 回だけ）。
func buildTemplate() error {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", templateDB)); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateDB); err != nil {
		return err
	}
	tplURL := withDB(adminURL, templateDB)
	conn, err := pgx.Connect(ctx, tplURL)
	if err != nil {
		return err
	}
	for _, f := range []string{"schema.sql", "seed_codes.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "db", f))
		if err != nil {
			conn.Close(ctx)
			return err
		}
		if err := execSimple(ctx, conn, string(raw)); err != nil {
			conn.Close(ctx)
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	conn.Close(ctx)
	var sink strings.Builder
	if code, err := applyMigrations(ctx, tplURL, false, &sink); err != nil || code != 0 {
		return fmt.Errorf("migrate: %v %s", err, sink.String())
	}
	dir, err := os.MkdirTemp("", "jobnavi-template-media-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	e := testEnvMap(map[string]string{
		"DATABASE_URL": tplURL, "DEMO_DATA": "allow", "STORAGE_BACKEND": "local", "STORAGE_LOCAL_DIR": dir, "IMAGE_PUBLIC_BASE_URL": "/media",
	})
	if code := seedMain([]string{"--extend"}, e); code != 0 {
		return fmt.Errorf("seed が失敗しました")
	}
	if code := seedImagesMain([]string{"--source-dir", mediaSource}, e); code != 0 {
		return fmt.Errorf("seed-images が失敗しました")
	}
	return nil
}

func skipWithoutDB(t *testing.T) {
	t.Helper()
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL が無いので、DB のテストをスキップします")
	}
	templateMu.Do(func() { templateErr = buildTemplate() })
	if templateErr != nil {
		t.Fatalf("テスト用の DB を作れませんでした: %v", templateErr)
	}
}

// cloneDB はテンプレートから複製した DB の URL を返す。テストの終わりに消す。
func cloneDB(t *testing.T) string {
	t.Helper()
	skipWithoutDB(t)
	ctx := context.Background()
	name := fmt.Sprintf("jobnavi_go_t_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB)); err != nil {
		t.Fatalf("DB を複製できません: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, adminURL)
		if err == nil {
			_, _ = c.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
			c.Close(ctx)
		}
	})
	return withDB(adminURL, name)
}

// ---- JWT の道具（偽の JWKS と署名）----

type jwtKit struct {
	ec     *ecdsa.PrivateKey
	rsa    *rsa.PrivateKey
	other  *ecdsa.PrivateKey
	server *httptest.Server
}

func newJWTKit(t *testing.T) *jwtKit {
	t.Helper()
	k := &jwtKit{}
	var err error
	if k.ec, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	if k.other, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	if k.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	pad := func(b []byte, n int) []byte { return append(make([]byte, n-len(b)), b...) }
	b64 := base64.RawURLEncoding.EncodeToString
	doc := map[string]any{"keys": []any{
		map[string]any{"kty": "EC", "crv": "P-256", "kid": "es1", "x": b64(pad(k.ec.X.Bytes(), 32)), "y": b64(pad(k.ec.Y.Bytes(), 32))},
		map[string]any{"kty": "RSA", "kid": "rs1", "n": b64(k.rsa.N.Bytes()), "e": b64(big.NewInt(int64(k.rsa.E)).Bytes())},
		map[string]any{"kty": "oct", "kid": "hs1", "k": "c2VjcmV0"},
		map[string]any{"kty": "EC", "crv": "P-384", "kid": "p384", "x": "AA", "y": "AA"},
	}}
	k.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(k.server.Close)
	return k
}

const testIssuer = "http://localhost:3200"

func baseClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"sub": "user_abcdefgh1", "email": "a@example.com", "email_verified": true, "name": "Ann", "picture": "https://img.example/a.png",
		"iss": testIssuer, "aud": "jobnavi", "exp": now + 900, "iat": now,
	}
}

// sign は JWT を作る。claims の値を nil にすると、その項目を外す。alg は ES256 / RS256 / HS256 / none。
func (k *jwtKit) sign(t *testing.T, claims map[string]any, alg, kid string, key any) string {
	t.Helper()
	body := map[string]any{}
	for name, v := range claims {
		if v != nil {
			body[name] = v
		}
	}
	header := map[string]any{"alg": alg}
	if kid != "" {
		header["kid"] = kid
	}
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	input := enc(header) + "." + enc(body)
	digest := sha256.Sum256([]byte(input))
	var sig []byte
	switch alg {
	case "ES256":
		priv := k.ec
		if p, ok := key.(*ecdsa.PrivateKey); ok {
			priv = p
		}
		r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	case "RS256":
		var err error
		if sig, err = rsa.SignPKCS1v15(rand.Reader, k.rsa, crypto.SHA256, digest[:]); err != nil {
			t.Fatal(err)
		}
	case "HS256":
		sig = []byte("not-a-real-signature-for-hs256")
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (k *jwtKit) token(t *testing.T, mod map[string]any) string {
	t.Helper()
	c := baseClaims()
	for name, v := range mod {
		c[name] = v
	}
	return k.sign(t, c, "ES256", "es1", nil)
}

// ---- サーバー ----

type testApp struct {
	t     *testing.T
	srv   *server
	pool  *pgxpool.Pool
	url   string
	media string
	jwt   *jwtKit
}

type appOpt func(m map[string]string)

// newApp は、複製した DB と、一時の保存先（local）で、server を作る。
func newApp(t *testing.T, opts ...appOpt) *testApp {
	t.Helper()
	dbURL := cloneDB(t)
	kit := newJWTKit(t)
	dir := t.TempDir()
	m := map[string]string{
		"DATABASE_URL": dbURL, "CORS_ORIGINS": "http://localhost:3200", "STORAGE_BACKEND": "local", "STORAGE_LOCAL_DIR": dir, "IMAGE_PUBLIC_BASE_URL": "/media",
		"AUTH_BASE_URL": testIssuer, "AUTH_JWKS_URL": kit.server.URL, "AUTH_AUDIENCE": "jobnavi", "SEARCH_ENGINE": "postgres",
	}
	for _, o := range opts {
		o(m)
	}
	cfg, err := loadConfig(testEnvMap(m))
	if err != nil {
		t.Fatalf("設定: %v", err)
	}
	ctx := context.Background()
	pool, err := openPool(ctx, cfg.DatabaseURL, 5)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	srv, err := newServer(cfg, pool, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return &testApp{t: t, srv: srv, pool: pool, url: dbURL, media: dir, jwt: kit}
}

type resp struct {
	Status int
	Body   string
	Header http.Header
}

func (r resp) JSON(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		t.Fatalf("JSON を読めません: %v\n%s", err, r.Body)
	}
}

// do は、ネットワークを通さずに、サーバーに要求する。
func (a *testApp) do(method, path string, headers map[string]string, body []byte) resp {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body == nil {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Result().Body)
	return resp{rec.Code, string(out), rec.Header()}
}

func (a *testApp) get(path string, headers map[string]string) resp {
	return a.do(http.MethodGet, path, headers, nil)
}

func bearerHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func jsonHeaders(extra map[string]string) map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func (a *testApp) exec(sql string, args ...any) {
	a.t.Helper()
	if _, err := a.pool.Exec(context.Background(), sql, args...); err != nil {
		a.t.Fatalf("SQL: %v\n%s", err, sql)
	}
}

func (a *testApp) queryInt(sql string, args ...any) int64 {
	a.t.Helper()
	var n int64
	if err := a.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		a.t.Fatalf("SQL: %v\n%s", err, sql)
	}
	return n
}

// addAuthUser は Better Auth の利用者の行を作る（users.auth_user_id の外部キーのため）。
func (a *testApp) addAuthUser(sub string) {
	a.t.Helper()
	a.exec(`INSERT INTO auth_user ("id", "name", "email", "emailVerified") VALUES ($1, 'x', $2, true) ON CONFLICT DO NOTHING`, sub, sub+"@auth.example")
}

// newEmptyDB は、db/schema.sql と seed_codes.sql だけを流した DB（Postgres の初回起動と同じ状態）を作る。テストの終わりに消す。
func newEmptyDB(t *testing.T) string {
	t.Helper()
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL が無いので、DB のテストをスキップします")
	}
	ctx := context.Background()
	name := fmt.Sprintf("jobnavi_go_e_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000_000)
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := pgx.Connect(ctx, adminURL); err == nil {
			_, _ = c.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
			c.Close(ctx)
		}
	})
	u := withDB(adminURL, name)
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, f := range []string{"schema.sql", "seed_codes.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "db", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := execSimple(ctx, conn, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

// captureStdout は fn が標準出力に書いたものを返す（CLI の出力の確認用）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}
