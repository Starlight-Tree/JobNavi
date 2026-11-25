package main

// 環境変数の読み取り。backend/app/settings.py の移植。設定ミス（整数でない・範囲外・URL でない）は、別の値に切り替えず、起動で止める
// （Python は一部をリクエストの途中で例外にしていたが、Go は起動で止める。値は出さない）。

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAuthAudience     = "jobnavi"
	defaultGardenMaxFail    = 5
	defaultGardenWindowMin  = 15
	defaultMeiliTimeoutSecs = 2
	defaultMaxUploadBytes   = 5 * 1024 * 1024
	defaultPort             = "8000"
	poolMaxConns            = 5
)

// env は環境変数の読み取り口（os.LookupEnv。テストで差し替える）。Python の os.environ.get と同じく、空文字で設定された値も「設定済み」と区別できる。
type env func(string) (string, bool)

func (e env) str(name string) string {
	v, _ := e(name)
	return v
}

type meiliConfig struct {
	URL     string
	Key     string // 空なら認証なし
	Timeout time.Duration
}

type authConfig struct {
	Issuer   string
	JWKSURL  string
	Audience string
}

type config struct {
	Port        string
	DatabaseURL string
	CORSOrigins []string

	StorageBackend string // local / s3
	StorageDir     string
	S3             s3Config // s3 のときだけ（4 つとも必須）
	PublicBaseURL  string   // 末尾の / なし

	Auth *authConfig // nil = ログインは使えない（503）

	GardenMaxFailures  int
	GardenWindowMinute int

	SearchEngine string // postgres / meilisearch
	Meili        *meiliConfig

	MaxUploadBytes int64
}

func (e env) httpURL(name string) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(e.str(name)), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("%s は http(s) の URL にしてください", name)
	}
	return raw, nil
}

func (e env) boundedInt(name string, def, low, high int) (int, error) {
	raw := strings.TrimSpace(e.str(name))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s は整数にしてください", name)
	}
	if v < low || v > high {
		return 0, fmt.Errorf("%s は %d から %d までの整数にしてください", name, low, high)
	}
	return v, nil
}

func loadConfig(e env) (config, error) {
	var c config
	c.Port = strings.TrimSpace(e.str("PORT"))
	if c.Port == "" {
		c.Port = defaultPort
	}
	c.DatabaseURL = e.str("DATABASE_URL")
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL が未設定です")
	}

	origins, ok := e("CORS_ORIGINS")
	if !ok {
		origins = "http://localhost:3000"
	}
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}

	c.StorageBackend = e.str("STORAGE_BACKEND")
	if c.StorageBackend == "" {
		c.StorageBackend = "local"
	}
	if c.StorageBackend != "local" && c.StorageBackend != "s3" {
		return c, errors.New("STORAGE_BACKEND は local か s3 にしてください")
	}
	if c.StorageBackend == "s3" {
		for _, f := range []struct {
			name string
			dst  *string
		}{{"STORAGE_S3_ENDPOINT_URL", &c.S3.Endpoint}, {"STORAGE_S3_BUCKET", &c.S3.Bucket}, {"STORAGE_S3_ACCESS_KEY_ID", &c.S3.KeyID}, {"STORAGE_S3_SECRET_ACCESS_KEY", &c.S3.Secret}} {
			if *f.dst = e.str(f.name); *f.dst == "" {
				return c, fmt.Errorf("%s が未設定です", f.name)
			}
		}
		if _, err := e.httpURL("STORAGE_S3_ENDPOINT_URL"); err != nil {
			return c, err
		}
	}
	c.StorageDir = e.str("STORAGE_LOCAL_DIR")
	if c.StorageDir == "" {
		c.StorageDir = "/data/media"
	}
	base, hasBase := e("IMAGE_PUBLIC_BASE_URL")
	if !hasBase && c.StorageBackend == "local" {
		base = "/media"
	}
	c.PublicBaseURL = strings.TrimRight(base, "/")
	if c.PublicBaseURL == "" {
		return c, errors.New("IMAGE_PUBLIC_BASE_URL が未設定です")
	}

	issuer, err := e.httpURL("AUTH_BASE_URL")
	if err != nil {
		return c, err
	}
	jwks, err := e.httpURL("AUTH_JWKS_URL")
	if err != nil {
		return c, err
	}
	if issuer != "" && jwks != "" {
		aud := strings.TrimSpace(e.str("AUTH_AUDIENCE"))
		if aud == "" {
			aud = defaultAuthAudience
		}
		c.Auth = &authConfig{Issuer: issuer, JWKSURL: jwks, Audience: aud}
	}

	if c.GardenMaxFailures, err = e.boundedInt("GARDEN_LOGIN_MAX_FAILURES", defaultGardenMaxFail, 1, 1000); err != nil {
		return c, err
	}
	if c.GardenWindowMinute, err = e.boundedInt("GARDEN_LOGIN_WINDOW_MINUTES", defaultGardenWindowMin, 1, 10080); err != nil {
		return c, err
	}

	c.SearchEngine = strings.TrimSpace(e.str("SEARCH_ENGINE"))
	if c.SearchEngine == "" {
		c.SearchEngine = "postgres"
	}
	if c.SearchEngine != "postgres" && c.SearchEngine != "meilisearch" {
		return c, errors.New("SEARCH_ENGINE は postgres か meilisearch にしてください")
	}
	if c.SearchEngine == "meilisearch" {
		if c.Meili, err = e.meili(); err != nil {
			return c, err
		}
	}

	c.MaxUploadBytes = defaultMaxUploadBytes
	if raw := strings.TrimSpace(e.str("IMAGE_MAX_UPLOAD_BYTES")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 1 {
			return c, errors.New("IMAGE_MAX_UPLOAD_BYTES は正の整数にしてください")
		}
		c.MaxUploadBytes = v
	}
	return c, nil
}

// meili は検索エンジンの設定（MEILI_URL が無ければ設定ミス）。キーは MEILI_API_KEY（無ければ空）。待ちは MEILI_TIMEOUT_SECONDS（既定 2）。
func (e env) meili() (*meiliConfig, error) {
	murl, err := e.httpURL("MEILI_URL")
	if err != nil {
		return nil, err
	}
	if murl == "" {
		return nil, errors.New("MEILI_URL が未設定です")
	}
	key := strings.TrimSpace(e.str("MEILI_API_KEY"))
	for _, r := range key { // HTTP のヘッダーに入れられない文字は、起動で止める（値は出さない）
		if r < 0x20 || r > 0x7e {
			return nil, errors.New("MEILI_API_KEY に、ASCII の印字できる文字以外が入っています")
		}
	}
	secs, err := e.boundedInt("MEILI_TIMEOUT_SECONDS", defaultMeiliTimeoutSecs, 1, 30)
	if err != nil {
		return nil, err
	}
	return &meiliConfig{URL: murl, Key: key, Timeout: time.Duration(secs) * time.Second}, nil
}
