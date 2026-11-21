-- 星庭ジョブ（hoshiniwa-job）: Postgres 17 のスキーマ（テーマは架空の宇宙の植物園・ガーデンの求人）
-- 名前は snake_case の英語、日時は timestamptz、真偽は boolean。
-- パスワードは平文ではなくハッシュ（argon2）を入れる。再設定用の確認コードは password_resets に分けた。

BEGIN;

-- ===== マスタ =====
CREATE TABLE areas (
  code        integer PRIMARY KEY,           -- エリアのコード
  name        text    NOT NULL,              -- 例: 翠環連邦内、第一環
  sort        integer NOT NULL,
  is_deleted  boolean NOT NULL DEFAULT false
);

CREATE TABLE stations (
  code        integer PRIMARY KEY,           -- 乗り場のコード
  name        text    NOT NULL,              -- 例: 白蔓線（軌道エレベーター）、ミルフェン中央駅
  sort        integer NOT NULL,
  is_deleted  boolean NOT NULL DEFAULT false
);

CREATE TABLE occupations (
  code        integer PRIMARY KEY,           -- 職種のコード
  category    text    NOT NULL,              -- 職種区分（栽培、解説・案内 など）
  name        text    NOT NULL,
  sort        integer NOT NULL,
  is_deleted  boolean NOT NULL DEFAULT false
);

CREATE TABLE employments (
  code        integer PRIMARY KEY,           -- 雇用形態のコード（1 常勤 / 2 短時間勤務 / 3 季節・臨時 / 4 派遣）
  name        text    NOT NULL,
  sort        integer NOT NULL,
  is_deleted  boolean NOT NULL DEFAULT false
);

-- ===== アカウント =====
CREATE TABLE users (                          -- 求職者
  id             integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  last_name      text,
  first_name     text,
  email          text NOT NULL UNIQUE,
  password_hash  text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  last_login_at  timestamptz
);

CREATE UNIQUE INDEX users_email_lower ON users (lower(email));   -- メールの大文字小文字を区別しない

CREATE TABLE user_sessions (                  -- ログイン中の印。トークンそのものではなく、そのハッシュ（sha256）だけを持つ
  token_hash  text PRIMARY KEY,
  user_id     integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_sessions_user ON user_sessions (user_id);

CREATE TABLE garden_accounts (                -- 園・法人の管理者
  id             integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  corporate      text NOT NULL,              -- 法人名
  email          text NOT NULL UNIQUE,
  password_hash  text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE garden_sessions (                -- 園のログイン中の印（利用者の user_sessions とは別。トークンはハッシュだけ持つ）
  token_hash         text PRIMARY KEY,
  garden_account_id  integer NOT NULL REFERENCES garden_accounts(id) ON DELETE CASCADE,
  expires_at         timestamptz NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX garden_sessions_garden ON garden_sessions (garden_account_id);

CREATE TABLE password_resets (                -- メールで送る確認コード（ハッシュで保存し、期限を付ける）
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  account_kind text NOT NULL CHECK (account_kind IN ('user', 'garden')),
  account_id  integer NOT NULL,
  code_hash   text NOT NULL,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_resets_account ON password_resets (account_kind, account_id);

-- ===== 求人 =====
CREATE TABLE recruits (                       -- 求人
  id            integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  garden_account_id integer REFERENCES garden_accounts(id) ON DELETE SET NULL,  -- 掲載している園のアカウント（園は自分の分だけ見られる）
  employ        smallint,                    -- 雇用形態
  job1          smallint, job2 smallint, job3 smallint, job4 smallint, job5 smallint,  -- 職種区分 1〜5（意味は入力画面の選択肢）
  category      text,                        -- 職種名
  corporate     text,                        -- 法人名
  profile       text,
  place         text,                        -- 所在地
  request       smallint,                    -- 求人内容
  experience    smallint,                    -- 業務経験
  salary        smallint,
  pay_min       integer,
  pay_max       integer,
  note1         text,
  worktime      smallint,
  start_hour    varchar(2), start_minute varchar(2), end_hour varchar(2), end_minute varchar(2),  -- 勤務の開始・終了時刻
  note2         text,
  overtime      smallint,
  expected      smallint,
  workday       varchar(30),
  note3         text,
  comment1      text,                        -- 業務内容
  dayoff        smallint,
  comment2      text,
  insurance     varchar(11),
  benefit       smallint,
  comment3      text,
  formfile      varchar(100),                -- 画像ファイル
  created_at    timestamptz NOT NULL DEFAULT now(),
  is_displayed  boolean NOT NULL DEFAULT false   -- サイトに表示するか
);

CREATE INDEX recruits_garden ON recruits (garden_account_id);

CREATE TABLE recruit_areas (
  recruit_id  integer NOT NULL REFERENCES recruits(id) ON DELETE CASCADE,
  area_code   integer NOT NULL REFERENCES areas(code),
  PRIMARY KEY (recruit_id, area_code)
);
CREATE TABLE recruit_stations (
  recruit_id    integer NOT NULL REFERENCES recruits(id) ON DELETE CASCADE,
  station_code  integer NOT NULL REFERENCES stations(code),
  PRIMARY KEY (recruit_id, station_code)
);
CREATE TABLE recruit_occupations (
  recruit_id       integer NOT NULL REFERENCES recruits(id) ON DELETE CASCADE,
  occupation_code  integer NOT NULL REFERENCES occupations(code),
  PRIMARY KEY (recruit_id, occupation_code)
);
CREATE TABLE recruit_employments (
  recruit_id       integer NOT NULL REFERENCES recruits(id) ON DELETE CASCADE,
  employment_code  integer NOT NULL REFERENCES employments(code),
  PRIMARY KEY (recruit_id, employment_code)
);
CREATE INDEX recruit_areas_code ON recruit_areas (area_code);
CREATE INDEX recruit_stations_code ON recruit_stations (station_code);
CREATE INDEX recruit_occupations_code ON recruit_occupations (occupation_code);
CREATE INDEX recruit_employments_code ON recruit_employments (employment_code);

-- ===== 応募（求職者情報）=====
CREATE TABLE entries (                        -- 応募
  id                 integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id            integer REFERENCES users(id) ON DELETE SET NULL,
  last_name          text, first_name text,
  last_name_kana     text, first_name_kana text,
  birth_date         date,                    -- 生年月日
  gender             smallint,
  postal_code        text,
  address1           text, address2 text, address3 text,
  phone1             text, phone2 text,
  email              text,
  desired_job        varchar(100),
  preferred_facility varchar(100),
  work_experience    text,
  hope_garden        text,
  comment            text,
  state              smallint,                -- 状態
  -- 担当者用のフラグ（ct1〜ct6）とそのコメント
  contacted          boolean NOT NULL DEFAULT false,   -- コンタクト済
  employed           boolean NOT NULL DEFAULT false,   -- 就業済
  caution            boolean NOT NULL DEFAULT false,   -- 要注意者
  declined           boolean NOT NULL DEFAULT false,   -- 辞退者
  rejected           boolean NOT NULL DEFAULT false,   -- 不採用
  no_response        boolean NOT NULL DEFAULT false,   -- 連絡無し
  flag_comments      jsonb NOT NULL DEFAULT '{}'::jsonb,  -- {"contacted": "...", ...}
  is_displayed       boolean NOT NULL DEFAULT true,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX entries_user ON entries (user_id);

-- ===== 検索用ビュー =====
-- 求人ごとに 1 行にして、条件の値は配列で持つ。
-- 注: この形（4 つの中間表の LEFT JOIN + GROUP BY）は、JOIN で行が掛け算になる。統計が無いと、見積もりが数億行になり、JIT が毎回走って、26 件でも 1 本 35〜140ms かかった。
--     0008 で、列ごとの ARRAY(SELECT …) に置き換えた（結果は同じ）。ここは 0001 の基準なので、定義そのものは変えない。
CREATE VIEW recruit_search AS
SELECT
  r.*,
  COALESCE(array_agg(DISTINCT ra.area_code)       FILTER (WHERE ra.area_code IS NOT NULL), '{}') AS area_codes,
  COALESCE(array_agg(DISTINCT rs.station_code)    FILTER (WHERE rs.station_code IS NOT NULL), '{}') AS station_codes,
  COALESCE(array_agg(DISTINCT ro.occupation_code) FILTER (WHERE ro.occupation_code IS NOT NULL), '{}') AS occupation_codes,
  COALESCE(array_agg(DISTINCT re.employment_code) FILTER (WHERE re.employment_code IS NOT NULL), '{}') AS employment_codes
FROM recruits r
LEFT JOIN recruit_areas       ra ON ra.recruit_id = r.id
LEFT JOIN recruit_stations    rs ON rs.recruit_id = r.id
LEFT JOIN recruit_occupations ro ON ro.recruit_id = r.id
LEFT JOIN recruit_employments re ON re.recruit_id = r.id
GROUP BY r.id;

COMMIT;
