-- ログインを Better Auth（画面のサーバー（Next.js）の中。docunavi と同じ版）へ移す。表を足すだけ（既存の行・値は変えない）。
-- 足すもの: (1) Better Auth の表 5 つ（名前は auth_ 付き。`frontend/src/lib/auth.ts` の modelName と同じ）、(2) users.auth_user_id（プロフィールと「だれか」の結び）。
-- ロック: 新しい表を作るだけ。users への ALTER は一瞬の ACCESS EXCLUSIVE（列の追加は表を書き換えない）。UNIQUE は、新しい列だけの索引を作る。
--
-- Better Auth の表の列名は、ライブラリが決めたまま（camelCase。引用符つき）にしてある。形は、同じ設定から `getMigrations(auth.options).compileMigrations()` で生成した SQL
-- （better-auth 1.7.18）。版を上げて列が変わるときは、新しい番号の SQL で足す（この SQL は書き換えない）。
-- auth_ の表は、画面のサーバー（Better Auth）だけが読み書きする。API（FastAPI）は読まない（JWT を JWKS で検証し、users だけを見る）。

-- 「だれか」。Google のアカウントごとに 1 行。id は Better Auth が作る文字列（JWT の sub になる）。lastName / firstName は Google の family_name / given_name（プロフィールの初期値）。
CREATE TABLE auth_user (
  "id"            text        NOT NULL PRIMARY KEY,
  "name"          text        NOT NULL,
  "email"         text        NOT NULL UNIQUE,
  "emailVerified" boolean     NOT NULL,
  "image"         text,
  "createdAt"     timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt"     timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "lastName"      text,
  "firstName"     text
);

-- ログイン中の印。token は Cookie の値（署名つき）の元になる。
CREATE TABLE auth_session (
  "id"        text        NOT NULL PRIMARY KEY,
  "expiresAt" timestamptz NOT NULL,
  "token"     text        NOT NULL UNIQUE,
  "createdAt" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt" timestamptz NOT NULL,
  "ipAddress" text,
  "userAgent" text,
  "userId"    text        NOT NULL REFERENCES auth_user ("id") ON DELETE CASCADE
);
CREATE INDEX auth_session_userId_idx ON auth_session ("userId");

-- 外のログイン元（Google）との結び。Google のアクセス・更新トークンは、アプリ側の設定（encryptOAuthTokens）で暗号化して入れる（ID トークンは、ライブラリの仕様で暗号化されない）。
CREATE TABLE auth_account (
  "id"                    text        NOT NULL PRIMARY KEY,
  "accountId"             text        NOT NULL,
  "providerId"            text        NOT NULL,
  "userId"                text        NOT NULL REFERENCES auth_user ("id") ON DELETE CASCADE,
  "accessToken"           text,
  "refreshToken"          text,
  "idToken"               text,
  "accessTokenExpiresAt"  timestamptz,
  "refreshTokenExpiresAt" timestamptz,
  "scope"                 text,
  "password"              text,
  "createdAt"             timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt"             timestamptz NOT NULL
);
CREATE INDEX auth_account_userId_idx ON auth_account ("userId");

-- ログインの途中の一時的な値（OAuth の state など）。期限つき。
CREATE TABLE auth_verification (
  "id"         text        NOT NULL PRIMARY KEY,
  "identifier" text        NOT NULL,
  "value"      text        NOT NULL,
  "expiresAt"  timestamptz NOT NULL,
  "createdAt"  timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  "updatedAt"  timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX auth_verification_identifier_idx ON auth_verification ("identifier");

-- JWT の署名鍵（ES256）。privateKey は Better Auth が BETTER_AUTH_SECRET で暗号化して入れる（Secret を変えると読めなくなる）。publicKey は /api/auth/jwks で公開される。
CREATE TABLE auth_jwks (
  "id"         text        NOT NULL PRIMARY KEY,
  "publicKey"  text        NOT NULL,
  "privateKey" text        NOT NULL,
  "createdAt"  timestamptz NOT NULL,
  "expiresAt"  timestamptz,
  "alg"        text,
  "crv"        text
);

-- 求職者のプロフィール（users）と、Better Auth の「だれか」（auth_user）の結び。JWT の sub がこの値になる。
-- 初めての JWT が来たときに、API が結ぶ（確認済みのメールが同じ既存の行があればそれに、なければ新しい行に）。auth_user を消すと、結びだけが外れる（プロフィールは残る）。
-- 前の版（0003）の google_sub は使わなくなった（既存の行・値は変えない。DROP はしない）。
ALTER TABLE users ADD COLUMN auth_user_id text UNIQUE REFERENCES auth_user ("id") ON DELETE SET NULL;
