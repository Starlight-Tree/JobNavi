package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 は、S3 互換の最小の偽物（パス形式。PUT・GET・HEAD・DELETE・ListObjectsV2）。署名（SigV4）を、受け取ったヘッダーから作り直して照合する。
type fakeS3 struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
	cache   map[string]string
	times   map[string]time.Time
	fail    func(method, key string) int // 0 以外なら、その状態を返す
	calls   []string
}

const (
	fakeKeyID  = "AKIDTEST"
	fakeSecret = "secret/with+chars"
	fakeBucket = "bucket"
)

func newFakeS3(t *testing.T) (*fakeS3, *s3Storage) {
	f := &fakeS3{t: t, objects: map[string][]byte{}, types: map[string]string{}, cache: map[string]string{}, times: map[string]time.Time{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	st := newS3Storage(s3Config{Endpoint: srv.URL, Bucket: fakeBucket, KeyID: fakeKeyID, Secret: fakeSecret})
	st.sleep = func(time.Duration) {}
	return f, st
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	// 署名の照合
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(body)
	if got := r.Header.Get("X-Amz-Content-Sha256"); got != hex.EncodeToString(sum[:]) {
		http.Error(w, "payload hash", http.StatusBadRequest)
		return
	}
	extra := map[string]string{}
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := r.Header.Get(h); v != "" {
			extra[h] = v
		}
	}
	if !strings.Contains(r.Header.Get("Authorization"), "SignedHeaders=") {
		http.Error(w, "no auth", http.StatusForbidden)
		return
	}
	want := signV4(fakeKeyID, fakeSecret, r.Method, r.Host, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("X-Amz-Content-Sha256"), signedExtra(r.Header.Get("Authorization"), extra), at)
	if want.authorization != r.Header.Get("Authorization") {
		f.t.Errorf("署名が合いません: %s %s\n got: %s\nwant: %s", r.Method, r.URL.String(), r.Header.Get("Authorization"), want.authorization)
		http.Error(w, "signature", http.StatusForbidden)
		return
	}
	prefix := "/" + fakeBucket
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if f.fail != nil {
		if code := f.fail(r.Method, key); code != 0 {
			http.Error(w, "injected", code)
			return
		}
	}
	switch {
	case key == "" && r.Method == http.MethodGet:
		f.list(w, r)
	case r.Method == http.MethodPut:
		f.objects[key], f.types[key], f.cache[key], f.times[key] = body, r.Header.Get("Content-Type"), r.Header.Get("Cache-Control"), time.Now().UTC()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// signedExtra は、署名に含めたヘッダー（Authorization の SignedHeaders）だけを、追加のヘッダーとして残す。
func signedExtra(auth string, extra map[string]string) map[string]string {
	out := map[string]string{}
	_, rest, _ := strings.Cut(auth, "SignedHeaders=")
	names, _, _ := strings.Cut(rest, ",")
	for _, n := range strings.Split(names, ";") {
		for k, v := range extra {
			if strings.EqualFold(k, n) {
				out[k] = v
			}
		}
	}
	return out
}

func (f *fakeS3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	start := 0
	if tok := q.Get("continuation-token"); tok != "" {
		fmt.Sscanf(tok, "t%d", &start)
	}
	const page = 2
	end := min(start+page, len(keys))
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><ListBucketResult>`)
	fmt.Fprintf(&b, "<IsTruncated>%v</IsTruncated>", end < len(keys))
	if end < len(keys) {
		fmt.Fprintf(&b, "<NextContinuationToken>t%d</NextContinuationToken>", end)
	}
	for _, k := range keys[start:end] {
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><LastModified>%s</LastModified></Contents>", k, f.times[k].Format(time.RFC3339))
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(b.String()))
}

func TestS3Storage(t *testing.T) {
	f, st := newFakeS3(t)
	ctx := context.Background()
	key := "images/0123-abc.jpg"
	data := []byte("hello image")
	if exists, err := st.Exists(ctx, key); err != nil || exists {
		t.Fatalf("無いキー: %v %v", exists, err)
	}
	if _, err := st.Get(ctx, key); !errors.Is(err, errStorageNotFound) {
		t.Errorf("無いキーの get: %v", err)
	}
	if err := st.Put(ctx, key, data, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if f.types[key] != "image/jpeg" || f.cache[key] != s3CacheControl {
		t.Errorf("保存したときのヘッダー: %q %q", f.types[key], f.cache[key])
	}
	if exists, err := st.Exists(ctx, key); err != nil || !exists {
		t.Errorf("あるキー: %v %v", exists, err)
	}
	if got, err := st.Get(ctx, key); err != nil || !bytes.Equal(got, data) {
		t.Errorf("get: %q %v", got, err)
	}
	// 同じ内容は何度でも（再送しない）。別の内容は上書きしない
	callsBefore := len(f.calls)
	if err := st.Put(ctx, key, data, "image/jpeg"); err != nil {
		t.Errorf("同じ内容の put: %v", err)
	}
	for _, c := range f.calls[callsBefore:] {
		if strings.HasPrefix(c, "PUT") {
			t.Error("同じ内容を、もう一度書きました")
		}
	}
	if err := st.Put(ctx, key, []byte("other"), "image/jpeg"); !errors.Is(err, errStorageConflict) {
		t.Errorf("別の内容の上書き: %v", err)
	}
	for _, bad := range []string{"../x", "A.jpg", "a//b", ""} {
		if err := st.Put(ctx, bad, data, "image/jpeg"); !errors.Is(err, errStorageBadKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// 一覧（2 件ずつの複数ページ）
	for i := 0; i < 5; i++ {
		_ = st.Put(ctx, fmt.Sprintf("images/k%d.png", i), []byte{byte(i)}, "image/png")
	}
	_ = st.Put(ctx, "other/x.png", []byte("x"), "image/png")
	keys, err := st.ListKeys(ctx, "images/")
	if err != nil || len(keys) != 6 {
		t.Fatalf("list: %d 件 %v", len(keys), err)
	}
	if keys[0].Modified.IsZero() || time.Since(keys[0].Modified) > time.Minute {
		t.Errorf("更新時刻: %v", keys[0].Modified)
	}
	if err := st.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, key); err != nil {
		t.Errorf("無いキーの削除も成功: %v", err)
	}
	if exists, _ := st.Exists(ctx, key); exists {
		t.Error("削除したのに残っています")
	}
}

func TestS3StorageErrorsAreFixedMessages(t *testing.T) {
	f, st := newFakeS3(t)
	ctx := context.Background()
	f.fail = func(method, key string) int { return http.StatusForbidden }
	checks := map[string]error{}
	_, checks["exists"] = st.Exists(ctx, "images/a.jpg")
	_, checks["get"] = st.Get(ctx, "images/a.jpg")
	checks["delete"] = st.Delete(ctx, "images/a.jpg")
	_, checks["list"] = st.ListKeys(ctx, "images/")
	want := map[string]error{"exists": errStorageHeadFail, "get": errStorageGetFailed, "delete": errStorageDelFailed, "list": errStorageListFail}
	for k, err := range checks {
		if !errors.Is(err, want[k]) {
			t.Errorf("%s: %v（期待 %v）", k, err, want[k])
		}
		if err != nil && (strings.Contains(err.Error(), fakeSecret) || strings.Contains(err.Error(), "127.0.0.1")) {
			t.Errorf("%s: エラーの文に接続先・秘密が入っています: %v", k, err)
		}
	}
	// 5xx は再試行する（3 回まで）。そのあと固定の文
	f.fail = func(method, key string) int { return http.StatusInternalServerError }
	f.calls = nil
	if _, err := st.Get(ctx, "images/a.jpg"); !errors.Is(err, errStorageGetFailed) || len(f.calls) != s3MaxAttempts {
		t.Errorf("再試行: %v 呼び出し %d 回", err, len(f.calls))
	}
	n := 0
	f.fail = func(method, key string) int {
		if method == http.MethodPut {
			n++
			if n < 3 {
				return http.StatusServiceUnavailable
			}
		}
		return 0
	}
	if err := st.Put(ctx, "images/b.jpg", []byte("x"), "image/jpeg"); err != nil || n != 3 {
		t.Errorf("2 回失敗して 3 回目で成功: %v（%d 回）", err, n)
	}
}

func TestLocalStorage(t *testing.T) {
	st := &localStorage{root: t.TempDir()}
	ctx := context.Background()
	if err := st.Put(ctx, "images/a.jpg", []byte("abc"), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "images/a.jpg", []byte("abc"), "image/jpeg"); err != nil {
		t.Errorf("同じ内容: %v", err)
	}
	if err := st.Put(ctx, "images/a.jpg", []byte("abd"), "image/jpeg"); !errors.Is(err, errStorageConflict) {
		t.Errorf("別の内容: %v", err)
	}
	if got, err := st.Get(ctx, "images/a.jpg"); err != nil || string(got) != "abc" {
		t.Errorf("get: %q %v", got, err)
	}
	if _, err := st.Get(ctx, "images/none.jpg"); !errors.Is(err, errStorageNotFound) {
		t.Errorf("無い: %v", err)
	}
	for _, bad := range []string{"../a.jpg", "images/../../a.jpg", "A.jpg", "/abs", "a//b.jpg"} {
		if _, err := st.Get(ctx, bad); !errors.Is(err, errStorageBadKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// 書きかけのファイル（.tmp-）は、一覧に出さない
	_ = os.WriteFile(filepath.Join(st.root, "public", "images", ".tmp-xyz"), []byte("x"), 0o644)
	_ = st.Put(ctx, "other/b.jpg", []byte("b"), "image/jpeg")
	keys, err := st.ListKeys(ctx, "images/")
	if err != nil || len(keys) != 1 || keys[0].Key != "images/a.jpg" {
		t.Errorf("list: %v %v", keys, err)
	}
	if err := st.Delete(ctx, "images/a.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(ctx, "images/a.jpg"); err != nil {
		t.Errorf("無いファイルの削除: %v", err)
	}
	if exists, _ := st.Exists(ctx, "images/a.jpg"); exists {
		t.Error("削除したのに残っています")
	}
}
