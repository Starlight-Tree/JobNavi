package main

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAuthTokenVerification(t *testing.T) {
	app := newApp(t)
	for _, sub := range []string{"user_abcdefgh1", "user_rs256_001", "user_other_001", "user_upper_001", "user_leeway_01", "user_audlist_1"} {
		app.addAuthUser(sub)
	}
	now := time.Now().Unix()
	kit := app.jwt
	token := func(mod map[string]any) string { return kit.token(t, mod) }
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"valid ES256", token(nil), 200},
		{"valid RS256", kit.sign(t, withClaims(map[string]any{"sub": "user_rs256_001", "email": "rs@example.com"}), "RS256", "rs1", nil), 200},
		{"exp within leeway", token(map[string]any{"sub": "user_leeway_01", "email": "lw@example.com", "exp": now - 5}), 200},
		{"exp beyond leeway", token(map[string]any{"exp": now - 15}), 401},
		{"nbf within leeway", token(map[string]any{"nbf": now + 5}), 200},
		{"nbf beyond leeway", token(map[string]any{"nbf": now + 20}), 401},
		{"iat in the future", token(map[string]any{"iat": now + 20}), 401},
		{"aud list contains", token(map[string]any{"sub": "user_audlist_1", "email": "al@example.com", "aud": []any{"x", "jobnavi"}}), 200},
		{"aud list lacks", token(map[string]any{"aud": []any{"x"}}), 401},
		{"wrong aud", token(map[string]any{"aud": "other"}), 401},
		{"wrong iss", token(map[string]any{"iss": "http://evil"}), 401},
		{"missing exp", token(map[string]any{"exp": nil}), 401},
		{"missing sub", token(map[string]any{"sub": nil}), 401},
		{"missing iss", token(map[string]any{"iss": nil}), 401},
		{"missing aud", token(map[string]any{"aud": nil}), 401},
		{"sub too short", token(map[string]any{"sub": "short"}), 401},
		{"sub bad chars", token(map[string]any{"sub": "user abcdefgh"}), 401},
		{"email unverified", token(map[string]any{"email_verified": false}), 401},
		{"email_verified as string", token(map[string]any{"email_verified": "true"}), 401},
		{"no email", token(map[string]any{"email": nil}), 401},
		{"email without @", token(map[string]any{"email": "abc"}), 401},
		{"signed with another key", kit.sign(t, baseClaims(), "ES256", "es1", kit.other), 401},
		{"unknown kid", kit.sign(t, baseClaims(), "ES256", "nokid", nil), 401},
		{"no kid", kit.sign(t, baseClaims(), "ES256", "", nil), 401},
		{"EC key with RS256 alg", kit.sign(t, baseClaims(), "RS256", "es1", nil), 401},
		{"RSA kid with ES256 alg", kit.sign(t, baseClaims(), "ES256", "rs1", nil), 401},
		{"P-384 key is ignored", kit.sign(t, baseClaims(), "ES256", "p384", nil), 401},
		{"HS256", kit.sign(t, baseClaims(), "HS256", "hs1", nil), 401},
		{"alg none", kit.sign(t, baseClaims(), "none", "es1", nil), 401},
		{"garbage", "a.b.c", 401},
		{"two parts", "a.b", 401},
	}
	for _, c := range cases {
		got := app.get("/api/auth/me", bearerHeader(c.token))
		if got.Status != c.want {
			t.Errorf("%s: %d（期待 %d）%s", c.name, got.Status, c.want, truncate(got.Body, 120))
		}
		if got.Status == 401 && got.Body != `{"detail":"ログインしてください"}` {
			t.Errorf("%s: 401 の本文は固定の文のはず: %s", c.name, got.Body)
		}
	}
	for _, h := range []string{"", "Bearer", "Bearer ", "Basic abc", "bearer", "Token abc"} {
		hdr := map[string]string{}
		if h != "" {
			hdr["Authorization"] = h
		}
		if got := app.get("/api/auth/me", hdr); got.Status != 401 {
			t.Errorf("Authorization %q: %d", h, got.Status)
		}
	}
	if got := app.get("/api/auth/me", map[string]string{"Authorization": "bearer " + token(nil)}); got.Status != 200 {
		t.Errorf("scheme は大文字小文字を区別しないはず: %d", got.Status)
	}
}

func withClaims(mod map[string]any) map[string]any {
	c := baseClaims()
	for k, v := range mod {
		c[k] = v
	}
	return c
}

func TestAuthNotConfigured(t *testing.T) {
	app := newApp(t, func(m map[string]string) { delete(m, "AUTH_BASE_URL"); delete(m, "AUTH_JWKS_URL") })
	got := app.get("/api/auth/me", bearerHeader("a.b.c"))
	if got.Status != 503 || !strings.Contains(got.Body, "Google でのログインは、まだ設定されていません") {
		t.Errorf("%d %s", got.Status, got.Body)
	}
	if got := app.get("/api/auth/me", nil); got.Status != 401 {
		t.Errorf("ヘッダーが無いときは、設定の有無より先に 401: %d", got.Status)
	}
}

func TestAuthJWKSUnavailable(t *testing.T) {
	app := newApp(t, func(m map[string]string) { m["AUTH_JWKS_URL"] = "http://127.0.0.1:1/jwks" })
	got := app.get("/api/auth/me", bearerHeader(app.jwt.token(t, nil)))
	if got.Status != 503 || !strings.Contains(got.Body, "ログインを確認できませんでした") {
		t.Errorf("%d %s", got.Status, got.Body)
	}
}

type userBody struct {
	ID          int32   `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

func TestUserProfileLifecycle(t *testing.T) {
	app := newApp(t)
	app.addAuthUser("user_abcdefgh1")
	app.addAuthUser("user_other_001")
	ann := map[string]any{"email": "Ann@Example.COM"}
	tok := app.jwt.token(t, ann)

	// はじめてのリクエストで行を作る。メールは小文字にし、表示名の初期値は Google の名前
	var u userBody
	r := app.get("/api/auth/me", bearerHeader(tok))
	r.JSON(t, &u)
	if r.Status != 200 || u.Email != "ann@example.com" || u.DisplayName == nil || *u.DisplayName != "Ann" || u.AvatarURL == nil || *u.AvatarURL != "https://img.example/a.png" {
		t.Fatalf("初回: %d %s", r.Status, r.Body)
	}
	if n := app.queryInt("SELECT count(*) FROM users WHERE auth_user_id = 'user_abcdefgh1'"); n != 1 {
		t.Fatalf("行が %d 件", n)
	}
	// 2 回目は同じ行（増えない）。画像が変われば更新、http の画像は持たない
	var u2 userBody
	app.get("/api/auth/me", bearerHeader(tok)).JSON(t, &u2)
	if u2.ID != u.ID || app.queryInt("SELECT count(*) FROM users WHERE auth_user_id = 'user_abcdefgh1'") != 1 {
		t.Errorf("2 回目で行が増えました: %+v", u2)
	}
	app.get("/api/auth/me", bearerHeader(app.jwt.token(t, map[string]any{"email": "Ann@Example.COM", "picture": "http://img.example/x.png"}))).JSON(t, &u2)
	if u2.AvatarURL != nil {
		t.Errorf("http の画像を持ちました: %v", *u2.AvatarURL)
	}
	// 別の Google アカウントで、同じメールは 409。既存の行（メールが同じで未結び）には結ぶ
	r = app.get("/api/auth/me", bearerHeader(app.jwt.token(t, map[string]any{"sub": "user_other_001", "email": "Ann@Example.COM"})))
	if r.Status != 409 || !strings.Contains(r.Body, "別の Google アカウントで使われています") {
		t.Errorf("競合: %d %s", r.Status, r.Body)
	}
	app.exec(`UPDATE users SET auth_user_id = NULL WHERE auth_user_id = 'user_abcdefgh1'`)
	r = app.get("/api/auth/me", bearerHeader(app.jwt.token(t, map[string]any{"sub": "user_other_001", "email": "Ann@Example.COM", "name": "Changed"})))
	var u3 userBody
	r.JSON(t, &u3)
	if r.Status != 200 || u3.ID != u.ID || u3.DisplayName == nil || *u3.DisplayName != "Ann" {
		t.Errorf("結び直し: %d %s（表示名は、すでにある値を上書きしない）", r.Status, r.Body)
	}

	// 表示名の変更
	hdr := jsonHeaders(bearerHeader(app.jwt.token(t, map[string]any{"sub": "user_other_001"})))
	patch := func(body string) resp { return app.do(http.MethodPatch, "/api/auth/me", hdr, []byte(body)) }
	if r := patch(`{"display_name":"  Taro  "}`); r.Status != 200 || !strings.Contains(r.Body, `"display_name":"Taro"`) {
		t.Errorf("変更: %d %s", r.Status, r.Body)
	}
	for _, body := range []string{`{"display_name":""}`, `{"display_name":"   "}`, `{"display_name":"` + strings.Repeat("あ", 31) + `"}`, `{"display_name":"a\nb"}`, `{"display_name":5}`, `{"display_name":null}`, `{}`, `[]`, `"x"`, ``} {
		if r := patch(body); r.Status != 422 {
			t.Errorf("%q: %d", body, r.Status)
		}
	}
	if r := patch(`{"display_name":"` + strings.Repeat("あ", 30) + `"}`); r.Status != 200 {
		t.Errorf("30 文字は通るはず: %d", r.Status)
	}
	if r := patch(`{`); r.Status != 422 || !strings.Contains(r.Body, `"json_invalid"`) || !strings.Contains(r.Body, "Expecting property name enclosed in double quotes") {
		t.Errorf("構文エラー: %d %s", r.Status, r.Body)
	}
	// 認証は、本文の検査より先（ただし JSON の構文は、認証より先）
	if r := app.do(http.MethodPatch, "/api/auth/me", jsonHeaders(nil), []byte(`{}`)); r.Status != 401 {
		t.Errorf("認証なしで本文が空のオブジェクト: %d", r.Status)
	}
	if r := app.do(http.MethodPatch, "/api/auth/me", jsonHeaders(nil), []byte(`{`)); r.Status != 422 {
		t.Errorf("認証なしで構文エラー: %d", r.Status)
	}
	if r := app.do(http.MethodPost, "/api/auth/me", hdr, nil); r.Status != 405 || r.Header.Get("Allow") != "GET" {
		t.Errorf("405: %d %s", r.Status, r.Header.Get("Allow"))
	}
}

func TestRecruitEventsAndRecommendations(t *testing.T) {
	app := newApp(t)
	for _, sub := range []string{"user_viewer_01", "user_viewer_02", "user_viewer_03"} {
		app.addAuthUser(sub)
	}
	login := func(sub string) map[string]string {
		return bearerHeader(app.jwt.token(t, map[string]any{"sub": sub, "email": sub + "@example.com"}))
	}
	u1, u2, u3 := login("user_viewer_01"), login("user_viewer_02"), login("user_viewer_03")
	post := func(id string, h map[string]string) int {
		return app.do(http.MethodPost, "/api/recruits/"+id+"/view", h, nil).Status
	}

	if post("1", nil) != 401 {
		t.Error("認証なしの記録は 401")
	}
	if got := post("1", u1); got != 204 {
		t.Fatalf("記録: %d", got)
	}
	if post("1", u1) != 204 || app.queryInt("SELECT count(*) FROM recruit_events WHERE recruit_id = 1") != 1 {
		t.Error("30 分以内の同じ閲覧は、足さずに 204")
	}
	app.exec(`UPDATE recruit_events SET created_at = now() - interval '31 minutes'`)
	post("1", u1)
	if n := app.queryInt("SELECT count(*) FROM recruit_events WHERE recruit_id = 1"); n != 2 {
		t.Errorf("31 分たてば、足す: %d", n)
	}
	for _, id := range []string{"99999", "abc", "1.5", "-1", "9999999999", "99999999999999999999"} {
		want := 404
		if id == "abc" || id == "1.5" {
			want = 422
		}
		if got := post(id, u1); got != want {
			t.Errorf("POST view %s: %d（期待 %d）", id, got, want)
		}
	}
	hidden := app.queryInt("SELECT id FROM recruits WHERE NOT is_displayed LIMIT 1")
	if got := post(itoa(hidden), u1); got != 404 {
		t.Errorf("非表示の求人は 404: %d", got)
	}

	// u1 は 1・2・3、u2 は 1・2・6・7、u3 は 2・6・8
	for _, id := range []string{"2", "3"} {
		post(id, u1)
	}
	for _, id := range []string{"1", "2", "6", "7"} {
		post(id, u2)
	}
	for _, id := range []string{"2", "6", "8"} {
		post(id, u3)
	}
	var rel []recruitSummary
	app.get("/api/recruits/1/related?limit=12", nil).JSON(t, &rel)
	if len(rel) != 12 || rel[0].ID != 2 {
		t.Errorf("関連: 件数 %d、先頭 %+v（共起が最も多い求人 2 が先頭のはず）", len(rel), firstID(rel))
	}
	for _, r := range rel {
		if r.ID == 1 {
			t.Error("自分自身を含めてはいけません")
		}
	}
	for _, c := range []struct {
		path string
		want int
	}{{"/api/recruits/1/related?limit=0", 422}, {"/api/recruits/1/related?limit=13", 422}, {"/api/recruits/1/related?limit=x", 422}, {"/api/recruits/99999/related", 404},
		{"/api/recruits/abc/related", 422}, {"/api/recruits/" + itoa(hidden) + "/related", 404}} {
		if got := app.get(c.path, nil).Status; got != c.want {
			t.Errorf("%s: %d（期待 %d）", c.path, got, c.want)
		}
	}
	var recs []recruitSummary
	r := app.get("/api/recommendations", u3)
	r.JSON(t, &recs)
	if r.Status != 200 || len(recs) == 0 || len(recs) > 6 {
		t.Fatalf("おすすめ: %d %s", r.Status, truncate(r.Body, 200))
	}
	seen := map[int32]bool{2: true, 6: true, 8: true}
	for _, rec := range recs {
		if seen[rec.ID] {
			t.Errorf("見た求人 %d をおすすめに出しました", rec.ID)
		}
	}
	if recs[0].ID != 1 && recs[0].ID != 7 && recs[0].ID != 3 {
		t.Errorf("共起の多い求人（1・7・3 のどれか）が先頭のはず: %d", recs[0].ID)
	}
	app.exec(`DELETE FROM recruit_events`)
	var none []recruitSummary
	app.get("/api/recommendations", u1).JSON(t, &none)
	if none == nil || len(none) != 0 {
		t.Errorf("履歴が無ければ空の配列: %v", none)
	}
	if got := app.get("/api/recommendations", nil).Status; got != 401 {
		t.Errorf("認証なし: %d", got)
	}
	if got := app.get("/api/recommendations?limit=13", u1).Status; got != 422 {
		t.Errorf("limit=13: %d", got)
	}
}

func firstID(l []recruitSummary) any {
	if len(l) == 0 {
		return nil
	}
	return l[0].ID
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEmbeddingsAffectRelatedOrder(t *testing.T) {
	app := newApp(t)
	ctx := t.Context()
	// 求人 1 の関連は、共起が無いとき、条件の一致の数 → ID の昇順。埋め込みを足すと、類似度 × 5 が得点に加わる
	var before []recruitSummary
	app.get("/api/recruits/1/related?limit=12", nil).JSON(t, &before)
	if err := setEmbedding(ctx, app.pool, 1, "m", []float64{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	last := before[len(before)-1].ID
	if err := setEmbedding(ctx, app.pool, last, "m", []float64{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var after []recruitSummary
	app.get("/api/recruits/1/related?limit=12", nil).JSON(t, &after)
	if after[0].ID != last {
		t.Errorf("類似度が 1 の求人 %d が先頭になるはず: %d", last, after[0].ID)
	}
	if err := setEmbedding(ctx, app.pool, 1, "", []float64{1}); err == nil {
		t.Error("空のモデル名を受け付けました")
	}
	if err := setEmbedding(ctx, app.pool, 1, "m", nil); err == nil {
		t.Error("空のベクトルを受け付けました")
	}
	if err := setEmbedding(ctx, app.pool, 1, "m", []float64{math.NaN()}); err == nil {
		t.Error("NaN を受け付けました")
	}
}
