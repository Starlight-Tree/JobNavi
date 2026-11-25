-- キーワード検索（q）の「ゆれ」を吸収する。求人の文字列と検索語を、同じ関数で同じ規則に正規化して比べる。
--   規則（search_fold）: NFKC（半角カナ→全角、全角の英数字・記号→半角、濁点の結合など）→ 小文字化 → ひらがな→カタカナ。
--   当たるようになる: ｶﾞｲﾄﾞ / ガイド / がいど、ﾐﾙﾌｪﾝ / ミルフェン、ＺＺＺ / zzz。当たらないまま（読みの辞書が要る）: ひらがな「おんしつ」→ 漢字「温室」。
-- 追加するもの（既存は DROP しない。recruit_search の既存の列は、名前・型・順序とも 0008 のまま。新しい列は末尾）:
--   search_fold(text)         … 規則そのもの（規則はここ 1 か所）。求人の文字列にも、検索語にも使う
--   recruit_search_text(×7)   … 検索の対象の列（職種名・法人名・紹介・所在地・業務内容 1〜3）をつなげて search_fold に通す
--   search_pattern(text)      … 検索語から LIKE の形（%語%）を作る。正規化してから % _ \ をエスケープする
--                                （全角の ％ ＿ ＼ は NFKC で % _ \ になるので、エスケープは正規化のあと）
--   recruits.search_text      … 上の結果を保存した列（GENERATED ALWAYS … STORED。書き込みのたびに DB が計算する）
--   recruit_search.search_text … その列を、ビューの末尾に出す。API は search_text LIKE search_pattern(語) で探す
-- なぜ保存するか（2026-11-22 の実測。開発 DB の中の TEMP 表、5,000 件）: クエリのたびに正規化すると 1 本 約 150ms（いまの ILIKE は約 5ms）。
--   重いのは translate（ひらがな→カタカナの 88 字の対応表。1 行 約 29µs）で、normalize は 1 行 約 3.7µs。保存した列を LIKE で探すと約 1.2ms（いまの ILIKE より速い）。
-- 注意:
--   ・recruit_search_text は IMMUTABLE と宣言している（生成列に使うため）。中の concat_ws は STABLE だが、引数が text だけなら結果は決まる。NULL の列を飛ばすので、STRICT にしない。
--   ・関数の本体（規則・対象の列）を変えたら、保存した列は古いまま残る。作り直すときは、新しいマイグレーションで
--     ALTER TABLE recruits ALTER COLUMN search_text SET EXPRESSION AS (…)（PostgreSQL 17 以降。全行を計算し直す）を流す。
--   ・DB の文字コードが UTF8 でないと normalize が動かない。違うときは、この下の DO で止まる。
--   ・関数の中から別の関数を呼ぶときは、public. を付ける。pg_restore は search_path を空にして行を入れ直すので、生成列の計算（recruit_search_text → search_fold）が、
--     付けないと「function search_fold(text) does not exist」で止まり、バックアップから戻せなくなる（tests/test_migrate.py の test_backup_and_restore_roundtrip が見張る）。
-- ロック: ALTER TABLE ... ADD COLUMN ... GENERATED STORED は、recruits を書き直す間、ACCESS EXCLUSIVE（実行器の lock_timeout は 5 秒。取れなければ失敗して何も変えない）。
--   5,000 行で約 0.18 秒（実測。TEMP 表）。5 万行なら約 1.8 秒（推測。行数に比例）。CREATE OR REPLACE VIEW / FUNCTION は一瞬。DROP はしない。
DO $$
BEGIN
  IF current_setting('server_encoding') <> 'UTF8' THEN
    RAISE EXCEPTION 'normalize() には UTF8 の DB が要ります';
  END IF;
END
$$;

CREATE OR REPLACE FUNCTION search_fold(text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
  SELECT translate(lower(normalize($1, NFKC)),
    'ぁあぃいぅうぇえぉおかがきぎくぐけげこごさざしじすずせぜそぞただちぢっつづてでとどなにぬねのはばぱひびぴふぶぷへべぺほぼぽまみむめもゃやゅゆょよらりるれろゎわゐゑをんゔゕゖゝゞ',
    'ァアィイゥウェエォオカガキギクグケゲコゴサザシジスズセゼソゾタダチヂッツヅテデトドナニヌネノハバパヒビピフブプヘベペホボポマミムメモャヤュユョヨラリルレロヮワヰヱヲンヴヵヶヽヾ')
$$;

CREATE OR REPLACE FUNCTION recruit_search_text(text, text, text, text, text, text, text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
  -- 引数: category, corporate, profile, place, comment1, comment2, comment3（NULL の列は、つなげるときに飛ばす）
  SELECT public.search_fold(concat_ws(' ', $1, $2, $3, $4, $5, $6, $7))
$$;

CREATE OR REPLACE FUNCTION search_pattern(text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
  SELECT '%' || replace(replace(replace(public.search_fold($1), '\', '\\'), '%', '\%'), '_', '\_') || '%'
$$;

ALTER TABLE recruits ADD COLUMN search_text text
  GENERATED ALWAYS AS (recruit_search_text(category, corporate, profile, place, comment1, comment2, comment3)) STORED;

CREATE OR REPLACE VIEW recruit_search AS
SELECT r.id,
    r.garden_account_id,
    r.employ,
    r.job1,
    r.job2,
    r.job3,
    r.job4,
    r.job5,
    r.category,
    r.corporate,
    r.profile,
    r.place,
    r.request,
    r.experience,
    r.salary,
    r.pay_min,
    r.pay_max,
    r.note1,
    r.worktime,
    r.start_hour,
    r.start_minute,
    r.end_hour,
    r.end_minute,
    r.note2,
    r.overtime,
    r.expected,
    r.workday,
    r.note3,
    r.comment1,
    r.dayoff,
    r.comment2,
    r.insurance,
    r.benefit,
    r.comment3,
    r.formfile,
    r.created_at,
    r.is_displayed,
    ARRAY( SELECT ra.area_code FROM recruit_areas ra WHERE ra.recruit_id = r.id ORDER BY ra.area_code) AS area_codes,
    ARRAY( SELECT rs.station_code FROM recruit_stations rs WHERE rs.recruit_id = r.id ORDER BY rs.station_code) AS station_codes,
    ARRAY( SELECT ro.occupation_code FROM recruit_occupations ro WHERE ro.recruit_id = r.id ORDER BY ro.occupation_code) AS occupation_codes,
    ARRAY( SELECT re.employment_code FROM recruit_employments re WHERE re.recruit_id = r.id ORDER BY re.employment_code) AS employment_codes,
    r.search_text
   FROM recruits r;
