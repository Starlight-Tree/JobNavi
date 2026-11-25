-- Google でのログイン。利用者の表（users）に、Google の ID とプロフィール画像の URL を足すだけ（既存の行・値は変えない。どちらも NULL 可・既定値なし）。
-- ロック: users への ALTER は一瞬の ACCESS EXCLUSIVE（列の追加は表を書き換えない）。UNIQUE は、新しい列だけの索引を作る。
-- パスワードを持たない利用者（Google だけでログインする人）の password_hash は、argon2 として読めない固定の印 '!' を入れる（NOT NULL は変えない。
-- 検証は必ず失敗するので、パスワードでは入れない）。この印は、アプリ（app/auth.py の NO_PASSWORD）が入れる。ここでは行を変えない。

-- google_sub は、Google が発行する利用者の ID（OpenID Connect の sub）。メールアドレスが変わっても同じ人と分かる。
ALTER TABLE users ADD COLUMN google_sub text UNIQUE;

-- avatar_url は、Google のプロフィール画像。https の URL だけ（画面は、https でなければ表示しない）。
ALTER TABLE users ADD COLUMN avatar_url text CHECK (avatar_url IS NULL OR (avatar_url LIKE 'https://%' AND char_length(avatar_url) <= 2048));
