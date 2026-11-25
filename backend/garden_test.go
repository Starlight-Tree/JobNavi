package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gardenEmail = "hoshiniwa-garden@example.com"

func gardenLogin(t *testing.T, app *testApp, email, password string) (string, resp) {
	t.Helper()
	r := app.do(http.MethodPost, "/api/garden/auth/login", jsonHeaders(nil), mustJSON(map[string]string{"email": email, "password": password}))
	var s gardenSession
	if r.Status == 200 {
		r.JSON(t, &s)
	}
	return s.Token, r
}

func loginOK(t *testing.T, app *testApp, email string) map[string]string {
	t.Helper()
	tok, r := gardenLogin(t, app, email, demoPassword)
	if r.Status != 200 || len(tok) != 43 {
		t.Fatalf("ログインできません: %d %s", r.Status, r.Body)
	}
	return bearerHeader(tok)
}

func TestGardenLoginAndSessions(t *testing.T) {
	app := newApp(t)
	if r := app.do(http.MethodPost, "/api/garden/auth/login", jsonHeaders(nil), mustJSON(map[string]string{"email": "nobody@example.com", "password": "x"})); r.Status != 401 || r.Body != `{"detail":"メールアドレスまたはパスワードが違います"}` {
		t.Errorf("登録の無いメール: %d %s", r.Status, r.Body)
	}
	if _, r := gardenLogin(t, app, gardenEmail, "wrong"); r.Status != 401 || r.Body != `{"detail":"メールアドレスまたはパスワードが違います"}` {
		t.Errorf("違うパスワード: %d %s（メールの有無で応答を分けない）", r.Status, r.Body)
	}
	tok, r := gardenLogin(t, app, " Hoshiniwa-Garden@Example.com ", demoPassword)
	if r.Status != 200 || !strings.Contains(r.Body, `"corporate":"環軌道植物公社"`) {
		t.Fatalf("前後の空白・大文字小文字は無視: %d %s", r.Status, r.Body)
	}
	auth := bearerHeader(tok)
	if n := app.queryInt("SELECT count(*) FROM garden_sessions WHERE token_hash = $1", digest(tok)); n != 1 || app.queryInt("SELECT count(*) FROM garden_sessions WHERE token_hash = $1", tok) != 0 {
		t.Error("トークンのハッシュだけを保存するはず")
	}
	if me := app.get("/api/garden/auth/me", auth); me.Status != 200 || !strings.Contains(me.Body, gardenEmail) {
		t.Errorf("me: %d %s", me.Status, me.Body)
	}
	// 利用者のトークン（JWT）では、園の API を開けない
	if got := app.get("/api/garden/auth/me", bearerHeader(app.jwt.token(t, nil))).Status; got != 401 {
		t.Errorf("JWT で園の me: %d", got)
	}
	var sum gardenSummary
	app.get("/api/garden/summary", auth).JSON(t, &sum)
	if sum.Displayed != 2 || sum.Hidden != 1 || sum.Corporate != "環軌道植物公社" {
		t.Errorf("summary: %+v", sum)
	}
	// 期限切れ・ログアウト
	app.exec(`UPDATE garden_sessions SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, digest(tok))
	if got := app.get("/api/garden/auth/me", auth).Status; got != 401 {
		t.Errorf("期限切れ: %d", got)
	}
	auth2 := loginOK(t, app, gardenEmail)
	if app.queryInt("SELECT count(*) FROM garden_sessions WHERE token_hash = $1", digest(tok)) != 0 {
		t.Error("ログインのついでに、期限切れのセッションを掃除するはず")
	}
	if got := app.do(http.MethodPost, "/api/garden/auth/logout", auth2, nil).Status; got != 204 {
		t.Errorf("logout: %d", got)
	}
	if got := app.do(http.MethodPost, "/api/garden/auth/logout", auth2, nil).Status; got != 204 {
		t.Errorf("2 回目の logout も 204: %d", got)
	}
	if got := app.get("/api/garden/auth/me", auth2).Status; got != 401 {
		t.Errorf("logout 後: %d", got)
	}
	if got := app.do(http.MethodPost, "/api/garden/auth/logout", nil, nil).Status; got != 401 {
		t.Errorf("ヘッダー無しの logout: %d", got)
	}
}

func TestGardenLoginValidationAndLockout(t *testing.T) {
	app := newApp(t)
	post := func(body string) resp {
		return app.do(http.MethodPost, "/api/garden/auth/login", jsonHeaders(nil), []byte(body))
	}
	for body, want := range map[string]int{
		``: 422, `abc`: 422, `[]`: 422, `{}`: 422, `{"email":"a@b.com"}`: 422, `{"password":"x"}`: 422, `{"email":5,"password":"x"}`: 422, `{"email":"abc","password":"x"}`: 422,
		`{"email":"a@b.com","password":""}`: 422, `{"email":"a@b.com","password":5}`: 422,
		`{"email":"a@b.com","password":"` + strings.Repeat("x", 129) + `"}`: 422, `{"email":"a@b.com","password":"` + strings.Repeat("x", 128) + `"}`: 401,
	} {
		if got := post(body).Status; got != want {
			t.Errorf("%.60q: %d（期待 %d）", body, got, want)
		}
	}
	// メールごとに数える。上限（5 回）を超えたら、正しいパスワードでも 429（登録の有無を分けない）
	for i := 0; i < 5; i++ {
		if _, r := gardenLogin(t, app, "locked@example.com", "bad"); r.Status != 401 {
			t.Fatalf("%d 回目: %d", i+1, r.Status)
		}
	}
	for _, email := range []string{"locked@example.com", " LOCKED@example.com"} {
		if _, r := gardenLogin(t, app, email, demoPassword); r.Status != 429 || !strings.Contains(r.Body, "ログインの試行が多すぎます") {
			t.Errorf("%q: %d %s", email, r.Status, r.Body)
		}
	}
	// 別のメールには及ばない。成功すると、そのメールの数えを消す
	for i := 0; i < 4; i++ {
		gardenLogin(t, app, gardenEmail, "bad")
	}
	if _, r := gardenLogin(t, app, gardenEmail, demoPassword); r.Status != 200 {
		t.Fatalf("上限の手前の成功: %d", r.Status)
	}
	if n := app.queryInt("SELECT count(*) FROM garden_login_attempts WHERE email_hash = $1", loginKey(gardenEmail)); n != 0 {
		t.Errorf("成功したら数えを消すはず: %d", n)
	}
	// 窓が過ぎたら数え直す
	app.exec(`UPDATE garden_login_attempts SET window_start = now() - interval '16 minutes' WHERE email_hash = $1`, loginKey("locked@example.com"))
	if _, r := gardenLogin(t, app, "locked@example.com", "bad"); r.Status != 401 {
		t.Errorf("窓が過ぎたあと: %d", r.Status)
	}
	if n := app.queryInt("SELECT failures FROM garden_login_attempts WHERE email_hash = $1", loginKey("locked@example.com")); n != 1 {
		t.Errorf("数え直して 1 のはず: %d", n)
	}
	if n := app.queryInt("SELECT count(*) FROM garden_login_attempts WHERE email_hash = 'locked@example.com'"); n != 0 {
		t.Error("生のメールを表に置いてはいけません")
	}
}

func TestGardenLoginRehashesWeakHash(t *testing.T) {
	app := newApp(t)
	weak := loadFixturesForApp(t)["rehash"]
	app.exec(`UPDATE garden_accounts SET password_hash = $1 WHERE email = $2`, weak, gardenEmail)
	if _, r := gardenLogin(t, app, gardenEmail, "secret-pw"); r.Status != 200 {
		t.Fatalf("弱いハッシュでもログインできるはず: %d", r.Status)
	}
	var stored string
	if err := app.pool.QueryRow(context.Background(), "SELECT password_hash FROM garden_accounts WHERE email = $1", gardenEmail).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == weak || needsRehash(stored) || !verifyPassword(stored, "secret-pw") {
		t.Errorf("新しいパラメーターのハッシュに更新するはず: %s", stored)
	}
}

func loadFixturesForApp(t *testing.T) map[string]string { return loadFixtures(t).Argon }

func TestGardenPermissions(t *testing.T) {
	app := newApp(t)
	a := loginOK(t, app, gardenEmail)
	b := loginOK(t, app, "hoshiniwa-garden2@example.com")
	var mineA, mineB []gardenRecruitItem
	app.get("/api/garden/recruits", a).JSON(t, &mineA)
	app.get("/api/garden/recruits", b).JSON(t, &mineB)
	if len(mineA) == 0 || len(mineB) == 0 || mineA[0].ID == mineB[0].ID {
		t.Fatalf("園ごとの求人: %v %v", mineA, mineB)
	}
	for _, c := range []struct {
		method, path string
		hdr          map[string]string
		want         int
	}{
		{"GET", "/api/garden/recruits/" + itoa(int64(mineB[0].ID)) + "/images", a, 404},
		{"GET", "/api/garden/recruits/99999/images", a, 404},
		{"GET", "/api/garden/recruits/abc/images", a, 422},
		{"GET", "/api/garden/recruits/" + itoa(int64(mineA[0].ID)) + "/images", nil, 401},
		{"GET", "/api/garden/recruits/" + itoa(int64(mineA[0].ID)) + "/images", a, 200},
		{"GET", "/api/garden/recruits/99999999999/images", a, 404},
		{"DELETE", "/api/garden/recruits/" + itoa(int64(mineB[0].ID)) + "/images/00000000-0000-0000-0000-000000000000", a, 404},
		{"DELETE", "/api/garden/recruits/" + itoa(int64(mineA[0].ID)) + "/images/zz", a, 422},
		{"DELETE", "/api/garden/recruits/" + itoa(int64(mineA[0].ID)) + "/images/order", a, 422},
		{"PUT", "/api/garden/recruits/" + itoa(int64(mineB[0].ID)) + "/images/order", jsonHeaders(a), 404},
	} {
		var body []byte
		if c.method == "PUT" {
			body = []byte(`{"image_ids":[]}`)
		}
		if got := app.do(c.method, c.path, c.hdr, body).Status; got != c.want {
			t.Errorf("%s %s: %d（期待 %d）", c.method, c.path, got, c.want)
		}
	}
	if r := app.get("/api/garden/recruits/"+itoa(int64(mineA[0].ID))+"/images", a); !strings.Contains(r.Body, `"max_images":10`) || !strings.Contains(r.Body, `"role":null`) {
		t.Errorf("一覧の形: %s", truncate(r.Body, 300))
	}
}

// ---- 画像 ----

type uploadResult struct {
	ID, Status string
	URL        *string `json:"url"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	MimeType   string  `json:"mime_type"`
	Position   int     `json:"position"`
	IsHero     bool    `json:"is_hero"`
	Role       *string `json:"role"`
	Origin     string  `json:"origin"`
	Alt        string  `json:"alt"`
}

func (a *testApp) upload(path string, hdr map[string]string, contentType string, data []byte) resp {
	a.t.Helper()
	h := map[string]string{"Content-Type": contentType}
	for k, v := range hdr {
		h[k] = v
	}
	return a.do(http.MethodPost, path, h, data)
}

func TestGardenRecruitImageLifecycle(t *testing.T) {
	app := newApp(t)
	a := loginOK(t, app, gardenEmail)
	rid := app.queryInt("SELECT r.id FROM recruits r JOIN garden_accounts g ON g.id = r.garden_account_id WHERE g.email = $1 AND NOT EXISTS (SELECT 1 FROM recruit_images WHERE recruit_id = r.id) ORDER BY r.id LIMIT 1", gardenEmail)
	base := "/api/garden/recruits/" + itoa(rid) + "/images"
	jpg := encodeJPEG(t, gradient(800, 600))
	png := encodePNG(t, gradient(500, 300))

	var ids []string
	for i, c := range []struct {
		data []byte
		ct   string
		w, h int
	}{{jpg, "image/jpeg", 800, 600}, {png, "image/png", 500, 300}, {encodeWebP(t, gradient(400, 400)), "image/webp", 400, 400}} {
		r := app.upload(base+"?alt="+urlEscape("画像"+itoa(int64(i))), a, c.ct, c.data)
		var u uploadResult
		r.JSON(t, &u)
		if r.Status != 201 || u.Status != "approved" || u.URL == nil || u.Position != i || u.IsHero || u.Origin != "uploaded" || u.Width != c.w || u.Height != c.h || u.MimeType != c.ct || u.Role != nil {
			t.Fatalf("登録 %d: %d %s", i, r.Status, r.Body)
		}
		if !strings.HasPrefix(*u.URL, "/media/images/"+u.ID+".") {
			t.Errorf("公開 URL: %s", *u.URL)
		}
		if _, err := os.Stat(filepath.Join(app.media, "public", "images", filepath.Base(*u.URL))); err != nil {
			t.Errorf("実体がありません: %v", err)
		}
		ids = append(ids, u.ID)
	}
	// 配信（ローカル保存のとき）
	m := app.get("/media/images/"+ids[0]+".jpg", nil)
	if m.Status != 200 || m.Header.Get("Content-Type") != "image/jpeg" || m.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(m.Header.Get("Cache-Control"), "immutable") || len(m.Body) == 0 {
		t.Errorf("media: %d %v", m.Status, m.Header)
	}
	for _, p := range []string{"/media/images/none.jpg", "/media/images/x.gif", "/media/x", "/media/../etc/passwd.jpg", "/media/IMAGES/a.jpg", "/media/"} {
		if r := app.get(p, nil); r.Status != 404 || r.Body != `{"detail":"画像が見つかりません"}` {
			t.Errorf("%s: %d %s", p, r.Status, r.Body)
		}
	}
	if r := app.do(http.MethodPost, "/media/images/x.jpg", nil, nil); r.Status != 405 {
		t.Errorf("POST /media: %d", r.Status)
	}

	// 受け付けないもの（保存も DB への記録も残さない）
	imagesBefore := app.queryInt("SELECT count(*) FROM images")
	for _, c := range []struct {
		name, path, ct string
		data           []byte
		want           int
		detail         string
	}{
		{"alt なし", base, "image/jpeg", jpg, 422, ""},
		{"alt が空白", base + "?alt=%20%20", "image/jpeg", jpg, 422, "代替テキストは 1〜300 文字で入れてください"},
		{"alt が長い", base + "?alt=" + urlEscape(strings.Repeat("あ", 301)), "image/jpeg", jpg, 422, "代替テキストは 1〜300 文字で入れてください"},
		{"Content-Type が画像でない", base + "?alt=x", "text/plain", jpg, 415, "JPEG・PNG・WebP の画像だけ登録できます"},
		{"Content-Type と中身の食い違い", base + "?alt=x", "image/png", jpg, 422, "画像の種類が、指定と一致しません"},
		{"空", base + "?alt=x", "image/jpeg", nil, 422, "画像が空です"},
		{"画像でない", base + "?alt=x", "image/jpeg", bytes.Repeat([]byte("abc"), 99), 422, msgImageUnreadable},
		{"小さい", base + "?alt=x", "image/png", encodePNG(t, gradient(100, 100)), 422, "画像は短辺 200px 以上にしてください"},
	} {
		r := app.upload(c.path, a, c.ct, c.data)
		if r.Status != c.want || (c.detail != "" && !strings.Contains(r.Body, c.detail)) {
			t.Errorf("%s: %d %s", c.name, r.Status, truncate(r.Body, 200))
		}
	}
	if r := app.upload(base+"?alt=x", nil, "image/jpeg", jpg); r.Status != 401 {
		t.Errorf("認証なし: %d", r.Status)
	}
	if n := app.queryInt("SELECT count(*) FROM images"); n != imagesBefore {
		t.Errorf("拒否した画像が DB に残りました: %d → %d", imagesBefore, n)
	}

	// ヒーロー・並べ替え
	put := func(path, body string) resp { return app.do(http.MethodPut, base+path, jsonHeaders(a), []byte(body)) }
	if r := put("/hero", `{"image_id":"`+ids[1]+`"}`); r.Status != 200 {
		t.Fatalf("hero: %d %s", r.Status, r.Body)
	}
	if r := put("/hero", `{"image_id":"`+ids[2]+`"}`); r.Status != 200 || app.queryInt("SELECT count(*) FROM recruit_images WHERE recruit_id = $1 AND is_hero", rid) != 1 {
		t.Errorf("ヒーローは 1 枚まで: %d", r.Status)
	}
	if r := put("/hero", `{"image_id":"00000000-0000-0000-0000-000000000000"}`); r.Status != 404 {
		t.Errorf("hero 不明: %d", r.Status)
	}
	for _, body := range []string{`{}`, `{"image_id":5}`, `{"image_id":"zz"}`, `{`} {
		if r := put("/hero", body); r.Status != 422 {
			t.Errorf("hero %q: %d", body, r.Status)
		}
	}
	rev := `{"image_ids":["` + ids[2] + `","` + ids[1] + `","` + ids[0] + `"]}`
	var order gardenRecruitImages
	r := put("/order", rev)
	r.JSON(t, &order)
	if r.Status != 200 || len(order.Images) != 3 || order.Images[0].ID != ids[2] || order.Images[2].ID != ids[0] || order.Images[0].Position != 0 {
		t.Fatalf("order: %d %s", r.Status, r.Body)
	}
	for _, body := range []string{`{"image_ids":["` + ids[0] + `"]}`, `{"image_ids":["` + ids[0] + `","` + ids[0] + `","` + ids[1] + `"]}`, `{"image_ids":[]}`} {
		if r := put("/order", body); r.Status != 422 || !strings.Contains(r.Body, "並べ替える画像が、今の画像の一覧と一致しません") {
			t.Errorf("order %q: %d %s", body, r.Status, r.Body)
		}
	}
	for _, body := range []string{`{"image_ids":"x"}`, `{"image_ids":[1]}`, `{}`} {
		if r := put("/order", body); r.Status != 422 {
			t.Errorf("order %q: %d", body, r.Status)
		}
	}
	if r := put("/order", `{"image_ids":["zz"]}`); r.Status != 422 || !strings.Contains(r.Body, "画像の指定が正しくありません") {
		t.Errorf("order 不正な uuid: %d %s", r.Status, r.Body)
	}

	// 外す: 参照が無くなった画像は、その場で deleted にして、実体も消す。並びは詰める
	key := app.queryString("SELECT storage_key FROM images WHERE id = $1::uuid", ids[1])
	if r := app.do(http.MethodDelete, base+"/"+ids[1], a, nil); r.Status != 204 {
		t.Fatalf("delete: %d %s", r.Status, r.Body)
	}
	if st := app.queryString("SELECT status FROM images WHERE id = $1::uuid", ids[1]); st != "deleted" {
		t.Errorf("状態: %s", st)
	}
	if _, err := os.Stat(filepath.Join(app.media, "public", key)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("実体が残っています: %v", err)
	}
	if got := app.queryInt("SELECT max(position) FROM recruit_images WHERE recruit_id = $1", rid); got != 1 {
		t.Errorf("並びを詰めるはず: 最大 %d", got)
	}
	if r := app.do(http.MethodDelete, base+"/"+ids[1], a, nil); r.Status != 404 {
		t.Errorf("もう一度: %d", r.Status)
	}

	// 運営の取り下げ: 一覧には残るが URL は出さない。ヒーローにできない
	s := &server{db: app.pool, store: app.srv.store, logf: func(string, ...any) {}}
	if err := s.rejectImage(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	var list gardenRecruitImages
	app.get(base, a).JSON(t, &list)
	var rejected *imageBody
	for i := range list.Images {
		if list.Images[i].ID == ids[0] {
			rejected = &list.Images[i]
		}
	}
	if rejected == nil || rejected.Status != "rejected" || rejected.URL != nil {
		t.Errorf("取り下げた画像: %+v", rejected)
	}
	if r := put("/hero", `{"image_id":"`+ids[0]+`"}`); r.Status != 409 {
		t.Errorf("取り下げた画像のヒーロー: %d", r.Status)
	}
	var pub struct {
		Images []imageInfo `json:"images"`
	}
	app.get("/api/recruits/"+itoa(rid), nil).JSON(t, &pub)
	for _, im := range pub.Images {
		if im.ID == ids[0] {
			t.Error("取り下げた画像を公開 API に出しています")
		}
	}
	if err := s.rejectImage(context.Background(), ids[0]); err == nil {
		t.Error("取り下げ済みを、もう一度取り下げられました")
	}
}

func TestGardenImageLimitsAndFailures(t *testing.T) {
	app := newApp(t, func(m map[string]string) { m["IMAGE_MAX_UPLOAD_BYTES"] = "60000" })
	a := loginOK(t, app, gardenEmail)
	rid := app.queryInt("SELECT r.id FROM recruits r JOIN garden_accounts g ON g.id = r.garden_account_id WHERE g.email = $1 AND NOT EXISTS (SELECT 1 FROM recruit_images WHERE recruit_id = r.id) ORDER BY r.id LIMIT 1", gardenEmail)
	base := "/api/garden/recruits/" + itoa(rid) + "/images"
	png := encodePNG(t, gradient(250, 250))
	if len(png) > 60000 {
		t.Fatalf("テスト画像が大きすぎます: %d", len(png))
	}
	// 大きすぎる本文（Content-Length の宣言・実際の長さのどちらでも 413）
	big := encodePNG(t, gradient(1500, 1500))
	if len(big) <= 60000 {
		big = append(big, make([]byte, 70000)...)
	}
	r := app.upload(base+"?alt=x", a, "image/png", big)
	if r.Status != 413 || !strings.Contains(r.Body, "画像は 0MB 以下にしてください") {
		t.Errorf("413: %d %s", r.Status, r.Body)
	}
	for i := 0; i < maxImagesPerRecruit; i++ {
		if r := app.upload(base+"?alt=x", a, "image/png", png); r.Status != 201 {
			t.Fatalf("%d 枚目: %d %s", i+1, r.Status, r.Body)
		}
	}
	files := countFiles(t, app.media)
	if r := app.upload(base+"?alt=over", a, "image/png", png); r.Status != 409 || !strings.Contains(r.Body, "10 枚まで") {
		t.Errorf("上限: %d %s", r.Status, r.Body)
	}
	if countFiles(t, app.media) != files || app.queryInt("SELECT count(*) FROM images WHERE alt = 'over'") != 0 {
		t.Error("上限を超えた画像が残りました")
	}

	// 保存先が失敗したら 503。作りかけの行も残さない
	rid2 := app.queryInt("SELECT r.id FROM recruits r JOIN garden_accounts g ON g.id = r.garden_account_id WHERE g.email = $1 AND r.id <> $2 ORDER BY r.id LIMIT 1", gardenEmail, rid)
	app.srv.store = failingStorage{app.srv.store}
	before := app.queryInt("SELECT count(*) FROM images")
	r = app.upload("/api/garden/recruits/"+itoa(rid2)+"/images?alt=x", a, "image/png", png)
	if r.Status != 503 || !strings.Contains(r.Body, "画像を保存できませんでした") {
		t.Errorf("保存先の失敗: %d %s", r.Status, r.Body)
	}
	if n := app.queryInt("SELECT count(*) FROM images"); n != before {
		t.Errorf("失敗した登録の行が残りました: %d → %d", before, n)
	}
}

type failingStorage struct{ storage }

func (failingStorage) Put(context.Context, string, []byte, string) error { return errStoragePutFailed }

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func (a *testApp) queryString(sql string, args ...any) string {
	a.t.Helper()
	var s string
	if err := a.pool.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		a.t.Fatalf("SQL: %v\n%s", err, sql)
	}
	return s
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(string("0123456789abcdef"[c>>4])+string("0123456789abcdef"[c&15])))
		}
	}
	return b.String()
}

func TestProfileImages(t *testing.T) {
	app := newApp(t)
	a := loginOK(t, app, gardenEmail)
	b := loginOK(t, app, "hoshiniwa-garden2@example.com")
	png := encodePNG(t, gradient(300, 300))
	const pi = "/api/garden/profile-images"
	if r := app.upload(pi+"?alt=x", a, "image/png", png); r.Status != 422 || !strings.Contains(r.Body, `"role"`) {
		t.Errorf("role なし: %d %s", r.Status, r.Body)
	}
	if r := app.upload(pi+"?alt=x&role=zzz", a, "image/png", png); r.Status != 422 || !strings.Contains(r.Body, "string_pattern_mismatch") {
		t.Errorf("role が不正: %d %s", r.Status, r.Body)
	}
	var first uploadResult
	for i := 0; i < maxProfileImages; i++ {
		r := app.upload(pi+"?alt=logo&role=logo", a, "image/png", png)
		var u uploadResult
		r.JSON(t, &u)
		if r.Status != 201 || u.Role == nil || *u.Role != "logo" || u.Position != i {
			t.Fatalf("logo %d: %d %s", i, r.Status, r.Body)
		}
		if i == 0 {
			first = u
		}
	}
	if r := app.upload(pi+"?alt=logo&role=logo", a, "image/png", png); r.Status != 409 || !strings.Contains(r.Body, "種類ごとに 5 枚まで") {
		t.Errorf("上限: %d %s", r.Status, r.Body)
	}
	if r := app.upload(pi+"?alt=banner&role=banner", a, "image/png", png); r.Status != 201 {
		t.Errorf("banner は別枠: %d", r.Status)
	}
	var mine, theirs []uploadResult
	app.get(pi, a).JSON(t, &mine)
	app.get(pi, b).JSON(t, &theirs)
	if len(mine) != 6 || len(theirs) != 0 || mine[0].Role == nil || *mine[0].Role != "banner" {
		t.Errorf("一覧（role → position の順）: %d 件 / %d 件", len(mine), len(theirs))
	}
	if r := app.do(http.MethodDelete, pi+"/"+first.ID, b, nil); r.Status != 404 {
		t.Errorf("他の園の画像: %d", r.Status)
	}
	if r := app.do(http.MethodDelete, pi+"/"+first.ID, a, nil); r.Status != 204 {
		t.Errorf("delete: %d", r.Status)
	}
	if r := app.do(http.MethodDelete, pi+"/"+first.ID, a, nil); r.Status != 404 {
		t.Errorf("もう一度: %d", r.Status)
	}
	if r := app.do(http.MethodDelete, pi+"/zz", a, nil); r.Status != 422 {
		t.Errorf("不正な uuid: %d", r.Status)
	}
}
