-- 閲覧の記録（求人の「関連」「おすすめ」の元データ）と、意味の近さ（埋め込み）の差し込み口。新しい表を 2 つ作るだけ（既存の表・行・値は変えない）。
-- ロック: 新しい表と索引の作成だけ。既存の表の書き込みは止めない（外部キーの参照先 users・recruits には、行ごとの共有ロックが一瞬かかるだけ）。
-- 個人情報: どの利用者がどの求人を見たかを保存する。利用者を消すと、その人の記録も消える（ON DELETE CASCADE）。説明は docs/recommendation.md。

-- 閲覧の記録。user_id は users.id（integer）に合わせる。event_type は今は 'view' だけ（あとで種類を足すときは、新しい番号の SQL で CHECK を替える）。
-- 同じ利用者・同じ求人でも、30 分を空ければ別の行になる（30 分以内の重複を足さないのは API の仕事。app/recruit_events.py）。
CREATE TABLE recruit_events (
  id          bigserial PRIMARY KEY,
  user_id     integer     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  recruit_id  integer     NOT NULL REFERENCES recruits(id) ON DELETE CASCADE,
  event_type  text        NOT NULL CHECK (event_type IN ('view')),
  created_at  timestamptz NOT NULL DEFAULT now()
);
-- 直近 30 分の確認（user_id, recruit_id, event_type で絞って created_at の新しい順）と、ある利用者が見た求人の一覧（先頭の user_id）に効く。
CREATE INDEX recruit_events_user_recruit ON recruit_events (user_id, recruit_id, event_type, created_at DESC);
-- 共起（同じ利用者が両方を見た）: ある求人を見た利用者の一覧（先頭の recruit_id）に効く。
CREATE INDEX recruit_events_recruit_user ON recruit_events (recruit_id, event_type, user_id);

-- 埋め込み（求人の文章の意味を数の並びにしたもの）の置き場。今は空で、使うときに値を入れる（入れる関数は app/recruit_embeddings.py の set_embedding だけ。呼ぶ CLI・API は無い）。
-- pgvector は使わない（拡張を足さない）。double precision[] に入れ、近さ（コサイン類似度）は、候補の少ない集合をアプリ側で計算する。
-- model は、ベクトルを作ったモデルの名前。モデルまたは次元が違うベクトルどうしは比べない（近さは 0 の扱い）。
CREATE TABLE recruit_embeddings (
  recruit_id  integer PRIMARY KEY REFERENCES recruits(id) ON DELETE CASCADE,
  model       text             NOT NULL CHECK (char_length(model) BETWEEN 1 AND 200),
  vector      double precision[] NOT NULL CHECK (cardinality(vector) > 0),
  updated_at  timestamptz      NOT NULL DEFAULT now()
);
