package main

// 画像の保存先。backend/app/storage.py の移植。
//   - local: フォルダー（root/public）。API の /media がそこを配る。テストとオフラインの開発用（本番では使わない）
//   - s3: S3 互換（Cloudflare R2 など）。バケットは 1 つで、置いた画像は公開 URL（CDN や r2.dev）から読める。署名（SigV4）は自前（s3sign.go）
// 署名付き URL は作らない。公開 URL は、保存キーから publicURL で組み立てる。エラーの文は固定（認証情報・キー・パスを含めない）。
// 同じキーへの別の内容の上書きはしない（画像の更新は、新しい画像として登録する）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type storageError struct{ msg string }

func (e *storageError) Error() string { return e.msg }

func newStorageError(msg string) error { return &storageError{msg} }

var (
	errStorageBadKey    = newStorageError("保存キーの形が正しくありません")
	errStorageConflict  = newStorageError("同じキーに別の内容は上書きできません")
	errStorageNotFound  = newStorageError("画像が見つかりません")
	errStoragePutFailed = newStorageError("画像の保存に失敗しました")
	errStorageGetFailed = newStorageError("画像の取得に失敗しました")
	errStorageHeadFail  = newStorageError("画像の確認に失敗しました")
	errStorageDelFailed = newStorageError("画像の削除に失敗しました")
	errStorageListFail  = newStorageError("画像の一覧の取得に失敗しました")
)

type storedKey struct {
	Key      string
	Modified time.Time
}

type storage interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
	ListKeys(ctx context.Context, prefix string) ([]storedKey, error)
}

// ---- local ----

type localStorage struct{ root string }

func (l *localStorage) base() string { return filepath.Join(l.root, "public") }

func (l *localStorage) path(key string) (string, error) {
	if _, err := checkKey(key); err != nil {
		return "", errStorageBadKey
	}
	base, err := filepath.Abs(l.base())
	if err != nil {
		return "", errStorageBadKey
	}
	p := filepath.Join(base, filepath.FromSlash(key))
	if rel, err := filepath.Rel(base, p); err != nil || strings.HasPrefix(rel, "..") {
		return "", errStorageBadKey
	}
	return p, nil
}

func (l *localStorage) Put(_ context.Context, key string, data []byte, _ string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(p); err == nil {
		if !bytes.Equal(existing, data) {
			return errStorageConflict
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return errStoragePutFailed
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-")
	if err != nil {
		return errStoragePutFailed
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return errStoragePutFailed
	}
	_ = os.Chmod(tmp.Name(), 0o644)
	if err := os.Rename(tmp.Name(), p); err != nil { // 書きかけのファイルを、公開される名前に置かない
		_ = os.Remove(tmp.Name())
		return errStoragePutFailed
	}
	return nil
}

func (l *localStorage) Get(_ context.Context, key string) ([]byte, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		return nil, errStorageNotFound
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, errStorageNotFound
	}
	return data, nil
}

func (l *localStorage) Exists(_ context.Context, key string) (bool, error) {
	p, err := l.path(key)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular(), nil
}

func (l *localStorage) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errStorageDelFailed
	}
	return nil
}

func (l *localStorage) ListKeys(_ context.Context, prefix string) ([]storedKey, error) {
	base := l.base()
	var out []storedKey
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return out, nil
	}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		out = append(out, storedKey{key, info.ModTime().UTC()})
		return nil
	})
	if err != nil {
		return nil, errStorageListFail
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ---- s3 ----

const (
	s3Region       = "auto"
	s3Service      = "s3"
	s3MaxAttempts  = 3 // boto3 の standard モードと同じ（最初の 1 回を含む）
	s3MaxBodyBytes = 64 << 20
	s3CacheControl = "public, max-age=31536000, immutable"
)

type s3Config struct {
	Endpoint string
	Bucket   string
	KeyID    string
	Secret   string
}

type s3Storage struct {
	cfg    s3Config
	client *http.Client
	now    func() time.Time
	sleep  func(time.Duration)
}

func newS3Storage(cfg s3Config) *s3Storage {
	return &s3Storage{cfg: cfg, client: &http.Client{Timeout: 35 * time.Second}, now: time.Now, sleep: time.Sleep}
}

// do は 1 回の要求（再試行つき）。5xx・429・届かないときは、少し待って s3MaxAttempts 回まで。
func (s *s3Storage) do(ctx context.Context, method, key string, query map[string]string, body []byte, headers map[string]string) (*http.Response, error) {
	base, err := url.Parse(s.cfg.Endpoint)
	if err != nil || base.Host == "" {
		return nil, errors.New("endpoint")
	}
	path := "/" + s.cfg.Bucket
	if key != "" {
		path += "/" + key
	}
	canonicalURI := uriEncode(path, false)
	canonicalQuery := canonicalQueryOf(query)
	payloadHash := emptyPayloadHash
	if body != nil {
		sum := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(sum[:])
	}
	var resp *http.Response
	for attempt := 1; ; attempt++ {
		sign := signV4(s.cfg.KeyID, s.cfg.Secret, method, base.Host, canonicalURI, canonicalQuery, payloadHash, headers, s.now())
		target := base.Scheme + "://" + base.Host + canonicalURI
		if canonicalQuery != "" {
			target += "?" + canonicalQuery
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, rerr := http.NewRequestWithContext(ctx, method, target, reader)
		if rerr != nil {
			return nil, rerr
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("X-Amz-Date", sign.amzDate)
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
		req.Header.Set("Authorization", sign.authorization)
		resp, err = s.client.Do(req)
		retryable := (err != nil && ctx.Err() == nil) || (err == nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500))
		if !retryable || attempt >= s3MaxAttempts {
			break
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
		}
		s.sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
	}
	return resp, err
}

func (s *s3Storage) Exists(ctx context.Context, key string) (bool, error) {
	if _, err := checkKey(key); err != nil {
		return false, errStorageBadKey
	}
	resp, err := s.do(ctx, http.MethodHead, key, nil, nil, nil)
	if err != nil {
		return false, errStorageHeadFail
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	}
	return false, errStorageHeadFail
}

func (s *s3Storage) Get(ctx context.Context, key string) ([]byte, error) {
	if _, err := checkKey(key); err != nil {
		return nil, errStorageBadKey
	}
	resp, err := s.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, errStorageGetFailed
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(io.LimitReader(resp.Body, s3MaxBodyBytes))
		if err != nil {
			return nil, errStorageGetFailed
		}
		return data, nil
	case http.StatusNotFound:
		return nil, errStorageNotFound
	}
	return nil, errStorageGetFailed
}

func (s *s3Storage) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if _, err := checkKey(key); err != nil {
		return errStorageBadKey
	}
	exists, err := s.Exists(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		existing, err := s.Get(ctx, key)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return errStorageConflict
		}
		return nil
	}
	resp, err := s.do(ctx, http.MethodPut, key, nil, data, map[string]string{"Content-Type": contentType, "Cache-Control": s3CacheControl})
	if err != nil {
		return errStoragePutFailed
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return errStoragePutFailed
	}
	return nil
}

func (s *s3Storage) Delete(ctx context.Context, key string) error {
	if _, err := checkKey(key); err != nil {
		return errStorageBadKey
	}
	resp, err := s.do(ctx, http.MethodDelete, key, nil, nil, nil)
	if err != nil {
		return errStorageDelFailed
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound { // 無いキーの削除も成功する
		return errStorageDelFailed
	}
	return nil
}

type s3ListResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

func (s *s3Storage) ListKeys(ctx context.Context, prefix string) ([]storedKey, error) {
	var out []storedKey
	token := ""
	for {
		query := map[string]string{"list-type": "2", "prefix": prefix}
		if token != "" {
			query["continuation-token"] = token
		}
		resp, err := s.do(ctx, http.MethodGet, "", query, nil, nil)
		if err != nil {
			return nil, errStorageListFail
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil || resp.StatusCode != http.StatusOK {
			return nil, errStorageListFail
		}
		var page s3ListResult
		if err := xml.Unmarshal(raw, &page); err != nil {
			return nil, errStorageListFail
		}
		for _, c := range page.Contents {
			t, err := time.Parse(time.RFC3339, c.LastModified)
			if err != nil {
				return nil, errStorageListFail
			}
			out = append(out, storedKey{c.Key, t.UTC()})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" {
			return out, nil
		}
		token = page.NextContinuationToken
	}
}

func newStorage(cfg config) (storage, error) {
	if cfg.StorageBackend == "local" {
		return &localStorage{root: cfg.StorageDir}, nil
	}
	if cfg.S3.Endpoint == "" || cfg.S3.Bucket == "" || cfg.S3.KeyID == "" || cfg.S3.Secret == "" {
		return nil, fmt.Errorf("STORAGE_S3_* の設定が足りません") // s3 のときは、1 つでも欠ければ失敗する（local に切り替えない）
	}
	return newS3Storage(cfg.S3), nil
}
