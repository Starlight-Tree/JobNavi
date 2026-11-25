package main

// 求職者のログイン状態の確認と、プロフィール（表示名）。backend/app/auth.py の移植。
// JWT（jwt.go）の sub を users.auth_user_id に結んで、求職者のプロフィール（users の行）を返す。はじめての人は、最初のリクエストで行を作る。
// 確認済みのメールが同じ既存の行があれば、新しい行は作らず、その行に結ぶ。エラーの本文は固定の文にする（例外の文字列は返さない）。

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	noPassword          = "!" // パスワードを持たない利用者（Google だけでログインする人）の password_hash。argon2 として読めない印
	userColumns         = "id, email, display_name, avatar_url"
	detailNotLoggedIn   = "ログインしてください"
	detailConflict      = "このメールアドレスは、別の Google アカウントで使われています"
	detailNotConfigured = "Google でのログインは、まだ設定されていません"
	detailUnavailable   = "ログインを確認できませんでした。しばらくしてから、もう一度お試しください"
)

type user struct {
	ID          int32   `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"` // 表示名（ニックネーム）。無ければ nil（画面はメールアドレスを出す）
	AvatarURL   *string `json:"avatar_url"`   // Google のプロフィール画像（https のみ）。無ければ nil
}

func scanUser(row pgx.Row) (user, error) {
	var u user
	return u, row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.AvatarURL)
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// profileFor は JWT の持ち主の users の行。結ばれていれば、その行。なければ、確認済みのメールが同じ既存の行に結ぶ。それも無ければ作る。
func profileFor(ctx context.Context, tx pgx.Tx, ident identity) (user, *httpError) {
	u, err := scanUser(tx.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE auth_user_id = $1", ident.Sub))
	if err == nil {
		// 毎回のリクエストでは書かない。画像が変わったときと、最後のログインが 1 時間以上前のときだけ
		if !equalPtr(u.AvatarURL, ident.Picture) {
			if _, err := tx.Exec(ctx, "UPDATE users SET avatar_url = $1 WHERE id = $2", ident.Picture, u.ID); err != nil {
				return u, wrapDB(err)
			}
			u.AvatarURL = ident.Picture
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET last_login_at = now() WHERE id = $1 AND (last_login_at IS NULL OR last_login_at < now() - interval '1 hour')", u.ID); err != nil {
			return u, wrapDB(err)
		}
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return u, wrapDB(err)
	}
	var (
		id         int32
		authUserID *string
	)
	err = tx.QueryRow(ctx, "SELECT id, auth_user_id FROM users WHERE lower(email) = $1", ident.Email).Scan(&id, &authUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return u, wrapDB(err)
	}
	found := err == nil
	if found && authUserID != nil && *authUserID != ident.Sub {
		// 同じメールの人が、すでに別の Google アカウントと結ばれている。乗っ取りを防ぐため、結び直さない
		return u, newHTTPError(http.StatusConflict, detailConflict)
	}
	if !found {
		u, err = scanUser(tx.QueryRow(ctx,
			"INSERT INTO users (display_name, email, password_hash, auth_user_id, avatar_url, last_login_at) VALUES ($1, $2, $3, $4, $5, now()) RETURNING "+userColumns,
			ident.Name, ident.Email, noPassword, ident.Sub, ident.Picture))
		return u, wrapDB(err)
	}
	// 既存の人: 結ぶ。表示名は、まだ空のときだけ Google の名前で埋める（本人が決めた名前を上書きしない）
	u, err = scanUser(tx.QueryRow(ctx,
		"UPDATE users SET auth_user_id = $1, avatar_url = $2, display_name = coalesce(display_name, $3), last_login_at = now() WHERE id = $4 RETURNING "+userColumns,
		ident.Sub, ident.Picture, ident.Name, id))
	return u, wrapDB(err)
}

// wrapDB は nil を nil のまま、それ以外を *httpError にする（一意制約違反は呼び出し側が見分けられるよう、原因を持たせる）。
func wrapDB(err error) *httpError {
	if err == nil {
		return nil
	}
	e := &httpError{status: http.StatusInternalServerError, detail: "Internal Server Error", cause: err}
	return e
}

func isUniqueViolation(e *httpError) bool {
	var pgErr *pgconn.PgError
	return e != nil && errors.As(e.cause, &pgErr) && pgErr.Code == "23505"
}

func bearer(r *http.Request) (string, *httpError) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "bearer") || token == "" {
		return "", newHTTPError(http.StatusUnauthorized, detailNotLoggedIn)
	}
	return token, nil
}

// currentUser はログイン中の利用者。トークンが無い・違う・期限切れなら 401。認証の設定が無ければ 503。
func (s *server) currentUser(ctx context.Context, r *http.Request) (user, *httpError) {
	var zero user
	token, e := bearer(r)
	if e != nil {
		return zero, e
	}
	if s.jwt == nil {
		return zero, newHTTPError(http.StatusServiceUnavailable, detailNotConfigured)
	}
	ident, err := s.jwt.verify(token)
	switch {
	case errors.Is(err, errAuthUnavailable):
		return zero, newHTTPError(http.StatusServiceUnavailable, detailUnavailable)
	case err != nil:
		s.logf("認証に失敗しました") // 失敗の種類だけ。トークンの中身は出さない
		return zero, newHTTPError(http.StatusUnauthorized, detailNotLoggedIn)
	}
	u, e := s.profileTx(ctx, ident)
	if isUniqueViolation(e) {
		// はじめてのリクエストが同時に 2 つ来た（同じ人）。もう一方が作った行を読む。見つからなければ、メールの競合
		u, err := scanUser(s.db.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE auth_user_id = $1", ident.Sub))
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, newHTTPError(http.StatusConflict, detailConflict)
		}
		if err != nil {
			return zero, s.dbFailure(err)
		}
		return u, nil
	}
	if e != nil {
		if e.cause != nil {
			return zero, s.dbFailure(e.cause)
		}
		return zero, e
	}
	return u, nil
}

// profileTx は profileFor を 1 つのトランザクションで行う。HTTP エラー（409）のときは巻き戻す。
func (s *server) profileTx(ctx context.Context, ident identity) (user, *httpError) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return user{}, wrapDB(err)
	}
	u, e := profileFor(ctx, tx, ident)
	if e != nil {
		_ = tx.Rollback(ctx)
		return u, e
	}
	if err := tx.Commit(ctx); err != nil {
		return u, wrapDB(err)
	}
	return u, nil
}

func (s *server) me(w http.ResponseWriter, r *http.Request) *httpError {
	ctx, cancel := dbContext(r)
	defer cancel()
	u, e := s.currentUser(ctx, r)
	if e != nil {
		return e
	}
	writeJSON(w, http.StatusOK, u, nil)
	return nil
}

// updateName は表示名（ニックネーム）の変更。変えられるのはログイン中の本人のものだけ（id は、JWT の持ち主から決まる）。
func (s *server) updateName(w http.ResponseWriter, r *http.Request) *httpError {
	// FastAPI と同じ順: 本文の JSON の構文 → 認証（401）→ 本文の検査（422）
	body, e := readJSONBody(r)
	if e != nil {
		return e
	}
	ctx, cancel := dbContext(r)
	defer cancel()
	u, e := s.currentUser(ctx, r)
	if e != nil {
		return e
	}
	name, e := validateUpdateName(body)
	if e != nil {
		return e
	}
	updated, err := scanUser(s.db.QueryRow(ctx, "UPDATE users SET display_name = $1 WHERE id = $2 RETURNING "+userColumns, name, u.ID))
	if err != nil {
		return s.dbFailure(err)
	}
	writeJSON(w, http.StatusOK, updated, nil)
	return nil
}
