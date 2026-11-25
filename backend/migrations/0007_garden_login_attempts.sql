-- 園（事業者）ログインの試行の制限に使う表。新しい表と索引を作るだけ（既存の表・行・値は変えない）。
-- ロック: 新しい表と索引の作成だけ。既存の表への書き込みは止めない。
-- 数え方（app/garden.py）: メールアドレスごとに、窓（既定 15 分。窓の最初の試行から）の中の試行を数える。上限（既定 5 回）を超えた試行は、正しいパスワードでも拒否する。
--   試行は、パスワードを確かめる前に数える（同時に大量に送られても、上限を超えて確かめない）。ログインに成功したら、そのメールの行を消す。
--   窓の 2 倍より古い行は、ログインのついでに消す。窓・上限は環境変数（app/settings.py）で変える。
-- IP アドレスでは数えない: API を呼ぶのは画面のサーバー（Next）で、どの利用者の通信も同じ送信元に見えるため。
-- 個人情報: 生のメールアドレスは置かない。正規化（前後の空白を除き、小文字にする）したメールの SHA-256（16 進 64 文字）だけを置く。
--   メールは推測しやすい文字列なので、このハッシュは「秘密」ではない（辞書で元に戻せることがある）。表を読める人を絞ること。生のメールが紛れ込まないよう、形を CHECK で縛る。
CREATE TABLE garden_login_attempts (
  email_hash    text        PRIMARY KEY CHECK (email_hash ~ '^[0-9a-f]{64}$'),
  failures      integer     NOT NULL CHECK (failures >= 1),  -- 窓の中で数えた試行（成功で行ごと消える）。上限 + 1 で頭打ち
  window_start  timestamptz NOT NULL DEFAULT now()           -- 窓の始まり。これから窓の長さが過ぎると、数え直す
);
-- 古い行の掃除（window_start の範囲で消す）に効く。
CREATE INDEX garden_login_attempts_window_start ON garden_login_attempts (window_start);
