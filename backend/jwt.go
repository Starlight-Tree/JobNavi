package main

// 求職者のログイン（画面のサーバーの中の Better Auth が発行した JWT の検証）。backend/app/jwt_auth.py の移植。
//
// ログイン（Google との通信・セッション・JWT の発行）は Better Auth（Next.js の中）の仕事で、この API は扱わない。
// 画面のサーバーが、ログイン中の人の JWT を Authorization: Bearer <JWT> で送ってくるので、ここで確かめる。
//   - 署名の検証は、Better Auth の JWKS（AUTH_JWKS_URL）の公開鍵（ES256 / RS256）だけで行う。alg=none や HS256 は受け付けない。
//     iss は AUTH_BASE_URL、aud は AUTH_AUDIENCE（既定 jobnavi）。exp iss aud sub は必須
//   - 利用者の ID は、検証に通ったトークンの sub だけから取る
//   - トークンの中身・Authorization ヘッダはログに出さない（失敗の種類だけ）
//   - 鍵を取れないときは、前に取れた鍵で続ける。前の鍵が無ければ、検証できない（呼び出し側は 503）。共有秘密の検証には切り替えない
//
// PyJWT と違う点: exp・nbf・iat が数値以外（文字列など）のトークンは、数値として読まず拒否する（署名つきなので Better Auth は出さない）。

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	jwtLeeway         = 10 * time.Second
	jwksFetchTimeout  = 5 * time.Second
	maxJWKSBytes      = 1_000_000
	jwksKeysTTL       = 600 * time.Second
	jwksMinRefreshGap = 60 * time.Second // 未知の kid を理由にした取り直しの最小間隔（でたらめな kid での増幅を防ぐ）
	jwksUserAgent     = "jobnavi-api/1.0"
	displayNameMax    = 30
	maxEmail          = 254
	maxPicture        = 2048
)

var (
	errInvalidToken      = errors.New("invalid token")
	errAuthNotConfigured = errors.New("auth not configured")
	errAuthUnavailable   = errors.New("auth unavailable")
)

// Better Auth の利用者 ID（英数字と _ -）
var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

type identity struct {
	Sub     string
	Email   string
	Name    *string // 表示名の初期値（Google の名前を 30 文字まで）。無ければ nil
	Picture *string
}

type jwk struct {
	alg string // ES256 / RS256
	ec  *ecdsa.PublicKey
	rsa *rsa.PublicKey
}

func b64(raw any) ([]byte, bool) {
	s, ok := raw.(string)
	if !ok {
		return nil, false
	}
	out, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return out, err == nil
}

// parseJWK は、使ってよい鍵（EC P-256 の ES256 / RSA の RS256）だけを返す。
func parseJWK(item map[string]any) (*jwk, bool) {
	alg, _ := item["alg"].(string)
	switch item["kty"] {
	case "EC":
		if item["crv"] != "P-256" || (alg != "" && alg != "ES256") {
			return nil, false
		}
		x, ok1 := b64(item["x"])
		y, ok2 := b64(item["y"])
		if !ok1 || !ok2 {
			return nil, false
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return nil, false
		}
		return &jwk{alg: "ES256", ec: pub}, true
	case "RSA":
		if alg != "" && alg != "RS256" {
			return nil, false
		}
		n, ok1 := b64(item["n"])
		e, ok2 := b64(item["e"])
		if !ok1 || !ok2 || len(n) == 0 || len(e) == 0 {
			return nil, false
		}
		ev := new(big.Int).SetBytes(e)
		if !ev.IsInt64() || ev.Int64() < 3 || ev.Int64() > 1<<31-1 {
			return nil, false
		}
		return &jwk{alg: "RS256", rsa: &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(ev.Int64())}}, true
	}
	return nil, false
}

func parseKeys(doc map[string]any) map[string]*jwk {
	keys := map[string]*jwk{}
	items, _ := doc["keys"].([]any)
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		kid, _ := m["kid"].(string)
		if kid == "" {
			continue
		}
		if k, ok := parseJWK(m); ok {
			keys[kid] = k
		}
	}
	return keys
}

// jwksCache は JWKS の公開鍵の置き場。期限が来たら取り直し、未知の kid は間隔を空けて取り直す。
type jwksCache struct {
	url    string
	client *http.Client
	logf   func(string, ...any)

	mu          sync.Mutex
	keys        map[string]*jwk
	fetchedAt   time.Time // 最後に取得に成功した時刻
	attemptedAt time.Time // 最後に取得を試みた時刻
}

func newJWKSCache(url string, logf func(string, ...any)) *jwksCache {
	return &jwksCache{url: url, client: &http.Client{Timeout: jwksFetchTimeout}, logf: logf, keys: map[string]*jwk{}}
}

func (c *jwksCache) fetch() (map[string]*jwk, error) {
	req, err := http.NewRequest(http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", jwksUserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errors.New("http status")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxJWKSBytes {
		return nil, errors.New("JWKS が大きすぎます")
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return parseKeys(doc), nil
}

func (c *jwksCache) key(kid string) (*jwk, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	expired := c.fetchedAt.IsZero() || now.Sub(c.fetchedAt) >= jwksKeysTTL
	if !expired {
		if k, ok := c.keys[kid]; ok {
			return k, nil
		}
	}
	recently := !c.attemptedAt.IsZero() && now.Sub(c.attemptedAt) < jwksMinRefreshGap
	if recently && len(c.keys) > 0 {
		return c.keys[kid], nil
	}
	c.attemptedAt = now
	keys, err := c.fetch()
	if err != nil {
		// 失敗の種類だけを残す（URL・本文は出さない）。前の鍵があれば、それで検証を続ける
		c.logf("JWKS を取得できませんでした (%T)", err)
		if len(c.keys) == 0 {
			return nil, errAuthUnavailable
		}
		return c.keys[kid], nil
	}
	c.keys = keys
	c.fetchedAt = time.Now()
	return c.keys[kid], nil
}

type jwtVerifier struct {
	cfg   authConfig
	cache *jwksCache
}

func newJWTVerifier(cfg authConfig, logf func(string, ...any)) *jwtVerifier {
	return &jwtVerifier{cfg: cfg, cache: newJWKSCache(cfg.JWKSURL, logf)}
}

func pyText(v any, limit int) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimFunc(s, isPySpace)
	n := utf8.RuneCountInString(s)
	return s, n > 0 && n <= limit
}

// displayNameFrom は Google の名前を、表示名の上限（30 文字。コードポイント）までにした値。空・文字列でなければ nil。
func displayNameFrom(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	var b strings.Builder
	for _, r := range s {
		if isPrintable(r) {
			b.WriteRune(r)
		}
	}
	out := strings.TrimFunc(b.String(), isPySpace)
	if runes := []rune(out); len(runes) > displayNameMax {
		out = string(runes[:displayNameMax])
	}
	out = strings.TrimFunc(out, isPySpace)
	if out == "" {
		return nil
	}
	return &out
}

// isPrintable は Python の str.isprintable() と同じ（印字できる文字。空白は ASCII の半角スペースだけ）。
func isPrintable(r rune) bool { return unicode.IsPrint(r) }

func identityFrom(claims map[string]any) (identity, error) {
	sub, _ := claims["sub"].(string)
	if !userIDPattern.MatchString(sub) {
		return identity{}, errInvalidToken
	}
	email, ok := pyText(claims["email"], maxEmail)
	// メールが確認済みでなければ、既存の人との結びつけに使えない（乗っ取りを防ぐ）。Google のメールは確認済み
	if !ok || !strings.Contains(email, "@") || claims["email_verified"] != true {
		return identity{}, errInvalidToken
	}
	id := identity{Sub: sub, Email: strings.ToLower(email), Name: displayNameFrom(claims["name"])}
	// 画面は https の画像だけを表示する。それ以外は持たない
	if pic, ok := pyText(claims["picture"], maxPicture); ok && strings.HasPrefix(pic, "https://") {
		id.Picture = &pic
	}
	return id, nil
}

func claimTime(claims map[string]any, name string) (float64, bool, bool) {
	raw, present := claims[name]
	if !present {
		return 0, false, true
	}
	f, ok := raw.(float64)
	return f, true, ok
}

func audienceMatches(claim any, want string) bool {
	switch v := claim.(type) {
	case string:
		return v == want
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// verify はトークンを検証して、ログイン中の人を返す。受け付けないときは errInvalidToken、鍵が取れないときは errAuthUnavailable。
func (v *jwtVerifier) verify(token string) (identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return identity{}, errInvalidToken
	}
	headerRaw, ok := b64(parts[0])
	if !ok {
		return identity{}, errInvalidToken
	}
	var header map[string]any
	if err := json.Unmarshal(headerRaw, &header); err != nil || header == nil {
		return identity{}, errInvalidToken
	}
	alg, _ := header["alg"].(string)
	kid, _ := header["kid"].(string)
	if alg != "ES256" && alg != "RS256" {
		return identity{}, errInvalidToken
	}
	if kid == "" {
		return identity{}, errInvalidToken
	}
	key, err := v.cache.key(kid)
	if err != nil {
		return identity{}, err
	}
	if key == nil || key.alg != alg {
		return identity{}, errInvalidToken
	}
	sig, ok := b64(parts[2])
	if !ok {
		return identity{}, errInvalidToken
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch alg {
	case "ES256":
		if len(sig) != 64 || !ecdsa.Verify(key.ec, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return identity{}, errInvalidToken
		}
	case "RS256":
		if rsa.VerifyPKCS1v15(key.rsa, crypto.SHA256, digest[:], sig) != nil {
			return identity{}, errInvalidToken
		}
	}
	payloadRaw, ok := b64(parts[1])
	if !ok {
		return identity{}, errInvalidToken
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadRaw, &claims); err != nil || claims == nil {
		return identity{}, errInvalidToken
	}
	for _, name := range []string{"exp", "iss", "aud", "sub"} {
		if _, ok := claims[name]; !ok {
			return identity{}, errInvalidToken
		}
	}
	now := float64(time.Now().UnixNano()) / 1e9
	leeway := jwtLeeway.Seconds()
	exp, _, ok := claimTime(claims, "exp")
	if !ok || exp <= now-leeway {
		return identity{}, errInvalidToken
	}
	if nbf, present, ok := claimTime(claims, "nbf"); !ok || (present && nbf > now+leeway) {
		return identity{}, errInvalidToken
	}
	if iat, present, ok := claimTime(claims, "iat"); !ok || (present && iat > now+leeway) {
		return identity{}, errInvalidToken
	}
	if iss, _ := claims["iss"].(string); iss != v.cfg.Issuer {
		return identity{}, errInvalidToken
	}
	if !audienceMatches(claims["aud"], v.cfg.Audience) {
		return identity{}, errInvalidToken
	}
	return identityFrom(claims)
}
