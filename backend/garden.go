package main

// 園・法人のログイン・ログアウトと、ダッシュボード用の件数。backend/app/garden.py の移植。
// 仕組みは利用者（auth.go）と同じ（部品は security.go）。ただし、アカウントもセッションも別の表で、利用者のトークンでは園の API を、園のトークンでは利用者の API を開けない。
// 園の会員登録の API は作らない（アカウントは運営が seed で作る）。園が見られるのは、自分のアカウントと、自分に紐づく求人（recruits.garden_account_id）だけ。
//
// ログインには、メールアドレスごとの試行の制限がある（garden_login_attempts。数え方は 0007 の SQL の冒頭）。IP では数えない
// （API を呼ぶのは画面のサーバーで、どの利用者も同じ送信元に見えるため）。窓の間の試行が上限を超えたら、正しいパスワードでも 429。
// 登録済みかどうかで応答を分けない（上限の応答も、メールが無いときの処理も、登録済みのときと同じ）。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type garden struct {
	ID        int32  `json:"id"`
	Email     string `json:"email"`
	Corporate string `json:"corporate"`
}

type gardenSession struct {
	Token  string `json:"token"`
	Garden garden `json:"garden"`
}

type gardenSummary struct {
	Corporate string `json:"corporate"`
	Email     string `json:"email"`
	Displayed int64  `json:"displayed"`
	Hidden    int64  `json:"hidden"`
}

type gardenRecruitItem struct {
	ID             int32   `json:"id"`
	Category       *string `json:"category"`
	IsDisplayed    bool    `json:"is_displayed"`
	ImageCount     int64   `json:"image_count"`
	PublishedCount int64   `json:"published_count"`
}

// currentGarden はログイン中の園。トークンが無い・違う・期限切れなら 401。
func (s *server) currentGarden(ctx context.Context, r *http.Request) (garden, *httpError) {
	var g garden
	token, e := bearer(r)
	if e != nil {
		return g, e
	}
	err := s.db.QueryRow(ctx,
		"SELECT g.id, g.email, g.corporate FROM garden_sessions s JOIN garden_accounts g ON g.id = s.garden_account_id "+
			"WHERE s.token_hash = $1 AND s.expires_at > now()", digest(token)).Scan(&g.ID, &g.Email, &g.Corporate)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, newHTTPError(http.StatusUnauthorized, detailNotLoggedIn)
	}
	if err != nil {
		return g, s.dbFailure(err)
	}
	return g, nil
}

func (s *server) gardenMe(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, g, nil)
	return nil
}

// gardenLogout は、送られたトークンが、すでに無効でも 204 を返す（ログアウトは何度呼んでも同じ結果）。ヘッダーが無いときは 401。
func (s *server) gardenLogout(w http.ResponseWriter, r *http.Request) *httpError {
	token, e := bearer(r)
	if e != nil {
		return e
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, "DELETE FROM garden_sessions WHERE token_hash = $1", digest(token)); err != nil {
		return s.dbFailure(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *server) gardenSummary(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	out := gardenSummary{Corporate: g.Corporate, Email: g.Email}
	if err := s.db.QueryRow(ctx,
		"SELECT count(*) FILTER (WHERE is_displayed), count(*) FILTER (WHERE NOT is_displayed) FROM recruits WHERE garden_account_id = $1", g.ID).Scan(&out.Displayed, &out.Hidden); err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, out, nil)
	return nil
}

func (s *server) gardenRecruits(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	g, e := s.currentGarden(ctx, r)
	if e != nil {
		return e
	}
	rows, err := s.db.Query(ctx,
		"SELECT r.id, r.category, r.is_displayed, "+
			"count(i.id) FILTER (WHERE i.status <> 'deleted') AS image_count, count(i.id) FILTER (WHERE i.status = 'approved') AS published_count "+
			"FROM recruits r LEFT JOIN recruit_images ri ON ri.recruit_id = r.id LEFT JOIN images i ON i.id = ri.image_id "+
			"WHERE r.garden_account_id = $1 GROUP BY r.id ORDER BY r.id", g.ID)
	if err != nil {
		return s.dbFailure(err)
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (gardenRecruitItem, error) {
		var it gardenRecruitItem
		return it, r.Scan(&it.ID, &it.Category, &it.IsDisplayed, &it.ImageCount, &it.PublishedCount)
	})
	if err != nil {
		return s.dbFailure(err)
	}
	if items == nil {
		items = []gardenRecruitItem{}
	}
	writeJSON(w, http.StatusOK, items, nil)
	return nil
}

// ---- ログイン ----

// loginKey は試行を数えるときの鍵。正規化（前後の空白を除き小文字）したメールの SHA-256。生のメールは表に置かない。
func loginKey(email string) string { return digest(strings.ToLower(strings.TrimSpace(email))) }

// reserveLoginAttempt は、このメールの試行を 1 つ数え、窓の中の上限の内なら true（上限を超えたら false）。パスワードを確かめる前に呼ぶ。
// 数えるのは 1 文の INSERT ... ON CONFLICT なので、同時に大量に送られても、上限を超えて確かめない。
// ログインの失敗の変更は巻き戻る（別のトランザクションで確定するので、数えた分は消えない）。
// 窓は、そのメールの最初の試行から（窓の長さが過ぎたら数え直す）。窓の 2 倍より古い行は、ここでついでに消す。
func (s *server) reserveLoginAttempt(ctx context.Context, emailHash string) (bool, error) {
	maxFailures, windowMinutes := int32(s.cfg.GardenMaxFailures), int32(s.cfg.GardenWindowMinute)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "DELETE FROM garden_login_attempts WHERE window_start < now() - make_interval(mins => $1::int * 2)", windowMinutes); err != nil {
		return false, err
	}
	var failures int32
	err = tx.QueryRow(ctx,
		"INSERT INTO garden_login_attempts AS a (email_hash, failures, window_start) VALUES ($1, 1, now()) "+
			"ON CONFLICT (email_hash) DO UPDATE SET "+
			"failures = CASE WHEN a.window_start <= now() - make_interval(mins => $2::int) THEN 1 ELSE LEAST(a.failures + 1, $3::int + 1) END, "+
			"window_start = CASE WHEN a.window_start <= now() - make_interval(mins => $2::int) THEN now() ELSE a.window_start END "+
			"RETURNING failures", emailHash, windowMinutes, maxFailures).Scan(&failures)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return failures <= maxFailures, nil
}

// validateLogin は本文（LoginRequest）を検査する。email: メールアドレス（前後の空白を除き小文字にして返す）、password: 1〜128 文字。
func validateLogin(b jsonBody) (email, password string, e *httpError) {
	obj, e := bodyObject(b)
	if e != nil {
		return "", "", e
	}
	var issues []issue
	if raw, present := obj["email"]; !present {
		issues = append(issues, issue{Type: "missing", Loc: []any{"body", "email"}, Msg: "Field required", Input: obj})
	} else if str, ok := raw.(string); !ok {
		issues = append(issues, issue{Type: "string_type", Loc: []any{"body", "email"}, Msg: "Input should be a valid string", Input: raw})
	} else if normalized, reason := validateEmail(str); reason != "" {
		issues = append(issues, issue{Type: "value_error", Loc: []any{"body", "email"}, Msg: emailPrefix + reason, Input: str, Ctx: map[string]any{"reason": reason}})
	} else {
		email = strings.ToLower(strings.TrimFunc(normalized, isPySpace))
	}
	if str, ok := stringField(obj, "password", &issues); ok {
		loc := []any{"body", "password"}
		switch n := utf8.RuneCountInString(str); {
		case n < 1:
			issues = append(issues, issue{Type: "string_too_short", Loc: loc, Msg: "String should have at least 1 character", Input: str, Ctx: map[string]any{"min_length": 1}})
		case n > 128:
			issues = append(issues, issue{Type: "string_too_long", Loc: loc, Msg: "String should have at most 128 characters", Input: str, Ctx: map[string]any{"max_length": 128}})
		default:
			password = str
		}
	}
	if len(issues) > 0 {
		return "", "", validationFail(issues...)
	}
	return email, password, nil
}

func (s *server) gardenLogin(w http.ResponseWriter, r *http.Request) *httpError {
	body, e := readJSONBody(r)
	if e != nil {
		return e
	}
	email, password, e := validateLogin(body)
	if e != nil {
		return e
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	ok, err := s.reserveLoginAttempt(ctx, loginKey(email))
	if err != nil {
		return s.dbFailure(err)
	}
	if !ok {
		return newHTTPError(http.StatusTooManyRequests, detailLoginLock) // 登録済みかどうか、パスワードが合っているかを調べない（正しくても拒否する）
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return s.dbFailure(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var (
		g      garden
		stored = s.dummyHash
		found  bool
	)
	var storedHash string
	switch err := tx.QueryRow(ctx, "SELECT id, email, corporate, password_hash FROM garden_accounts WHERE lower(email) = $1", email).Scan(&g.ID, &g.Email, &g.Corporate, &storedHash); {
	case err == nil:
		found, stored = true, storedHash
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return s.dbFailure(err)
	}
	// メールが無いときも同じだけ時間をかけ、応答の速さから登録の有無を読まれないようにする
	if !verifyPassword(stored, password) || !found {
		return newHTTPError(http.StatusUnauthorized, detailBadLogin) // 数えた試行は、そのまま残る
	}
	if _, err := tx.Exec(ctx, "DELETE FROM garden_login_attempts WHERE email_hash = $1", loginKey(email)); err != nil { // 成功したら、そのメールの数えを消す
		return s.dbFailure(err)
	}
	if needsRehash(stored) { // 設定を強めたとき、次のログインで新しいハッシュに更新する
		if _, err := tx.Exec(ctx, "UPDATE garden_accounts SET password_hash = $1 WHERE id = $2", hashPassword(password), g.ID); err != nil {
			return s.dbFailure(err)
		}
	}
	token := newToken()
	if _, err := tx.Exec(ctx, "DELETE FROM garden_sessions WHERE expires_at < now()"); err != nil { // 期限切れの掃除（呼ばれたときについでに）
		return s.dbFailure(err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO garden_sessions (token_hash, garden_account_id, expires_at) VALUES ($1, $2, now() + make_interval(days => $3::int))",
		digest(token), g.ID, int32(sessionDays)); err != nil {
		return s.dbFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, gardenSession{Token: token, Garden: g}, nil)
	return nil
}
