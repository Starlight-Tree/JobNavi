-- 検索用ビュー recruit_search を、4 つの中間表の LEFT JOIN + GROUP BY から、配列の列ごとの相関サブクエリ（ARRAY(SELECT …)）に置き換える。
-- 理由: JOIN で行が掛け算になる形は、統計が無い（または少ない）と、プランナーが「求人 1 件につき、各中間表が 11 行」と見積もり、
--   26 件でも結合の行数を約 8.4 億行・コスト約 380 万と見て、JIT コンパイル（jit_above_cost の既定は 10 万）を毎回走らせる。実際の行は 40。
--   結果、一覧の検索 1 回（SQL 2〜3 本）と条件ごとの件数（SQL 5 本）が、26 件でも、1 本あたり約 35〜140ms かかっていた。
-- 結果は同じ: 列の名前・型・順序は変えない。配列の中身は、昇順・重複なし（中間表は主キーが (recruit_id, コード) なので、重複しない）。
-- 列は、いまのビューの並びのまま書き出す（r.* のままだと、recruits にあとから足された列が、配列の列の前に入って、CREATE OR REPLACE VIEW が通らない）。
-- ロック: CREATE OR REPLACE VIEW は一瞬。DROP はしない。
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
    ARRAY( SELECT re.employment_code FROM recruit_employments re WHERE re.recruit_id = r.id ORDER BY re.employment_code) AS employment_codes
   FROM recruits r;
