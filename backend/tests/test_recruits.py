import psycopg
import pytest

from tests.test_garden import db_exec


def test_health(client):
    assert client.get("/health").json() == {"status": "ok"}


def test_master_has_codes_and_grouped_occupations(client):
    body = client.get("/api/master").json()
    assert len(body["areas"]) == 14
    assert len(body["stations"]) == 11
    assert len(body["employments"]) == 4
    assert body["occupations"][0]["category"] == "栽培"
    assert sum(len(g["items"]) for g in body["occupations"]) == 35


def test_search_without_filters_shows_only_displayed(client):
    body = client.get("/api/recruits").json()
    assert body["total"] == 11  # 非表示の 1 件は出ない
    assert all(item["corporate"] != "非表示のテスト求人" for item in body["items"])


def test_search_or_within_kind_and_between_kinds(client):
    # 地域 3(第一環) か 4(第二環) の求人
    two = client.get("/api/recruits", params={"area": [3, 4]}).json()
    assert {i["corporate"] for i in two["items"]} == {"環軌道植物公社", "星庭財団"}
    # さらに雇用形態が 1(常勤) に限る（AND）
    narrowed = client.get("/api/recruits", params={"area": [3, 4], "employment": 1}).json()
    assert narrowed["total"] == 3
    assert all("常勤" in i["employments"] for i in narrowed["items"])


def test_paging(client):
    first = client.get("/api/recruits", params={"per_page": 5}).json()
    assert (first["total"], first["last_page"], len(first["items"])) == (11, 3, 5)
    last = client.get("/api/recruits", params={"per_page": 5, "page": 3}).json()
    assert len(last["items"]) == 1


def test_detail_and_hidden_404(client):
    first = client.get("/api/recruits").json()["items"][0]
    detail = client.get(f"/api/recruits/{first['id']}").json()
    assert detail["start_time"] == "09:00" and detail["areas"]
    assert client.get("/api/recruits/12").status_code == 404  # 非表示
    assert client.get("/api/recruits/9999").status_code == 404


def test_invalid_page_is_rejected(client):
    assert client.get("/api/recruits", params={"page": 0}).status_code == 422
    assert client.get("/api/recruits", params={"per_page": 999}).status_code == 422


def _counts(rows):
    return {r["code"]: r["count"] for r in rows}


def test_facets_without_filters_count_displayed_only(client):
    body = client.get("/api/facets").json()
    assert body["total"] == 11
    assert _counts(body["employments"]) == {1: 5, 2: 3, 3: 2, 4: 1}  # 非表示の常勤 1 件は数えない
    assert _counts(body["areas"])[3] == 2


def test_facets_ignore_own_kind_but_apply_other_kinds(client):
    # 常勤だけに絞ると、地域の件数は「常勤の求人」から数える
    areas = _counts(client.get("/api/facets", params={"employment": 1}).json()["areas"])
    assert areas == {3: 1, 4: 2, 9: 1, 10: 1, 12: 1}
    # 雇用形態の件数は、自分の種類（雇用形態）の選択に影響されない。他の種類（地域 3,4）は効く
    both = client.get("/api/facets", params={"area": [3, 4], "employment": 1}).json()
    assert both["total"] == 3
    assert _counts(both["employments"]) == {1: 3, 3: 1}


def test_summary_has_salary_kind(client):
    items = client.get("/api/recruits").json()["items"]
    assert items[0]["salary"] == 2 and items[0]["pay_min"] == 180000  # 月給制
    assert items[2]["salary"] == 4 and items[2]["pay_min"] == 1100  # 時給制


def test_every_summary_has_a_hero_image_key(client):
    items = client.get("/api/recruits", params={"per_page": 50}).json()["items"]
    assert len(items) == 11
    for item in items:
        assert "hero_image" in item  # 画像が無い求人でも、キーは必ずある（値は null）
        assert item["hero_image"] is None or set(item["hero_image"]) == {"id", "url", "alt", "width", "height", "mime_type"}


# ===== キーワード検索（q） =====


def test_keyword_matches_title_and_corporate(client):
    by_title = client.get("/api/recruits", params={"q": "語り部"}).json()
    assert {i["category"] for i in by_title["items"]} == {"植物園の語り部", "遺跡前の語り部"}
    by_corporate = client.get("/api/recruits", params={"q": "環軌道植物公社"}).json()
    assert by_corporate["total"] == 2 and {i["corporate"] for i in by_corporate["items"]} == {"環軌道植物公社"}


def test_keyword_terms_are_anded_across_columns_and_split_on_any_space(client):
    # 半角・全角の空白で区切る。語の間は AND（職種の語と法人の語をまたいでよい）
    for q in ("語り部 遺跡", "語り部　遺跡"):
        assert client.get("/api/recruits", params={"q": q}).json()["total"] == 1
    cross = client.get("/api/recruits", params={"q": "語り部 ミルフェン"}).json()
    assert [i["category"] for i in cross["items"]] == ["植物園の語り部"]
    assert client.get("/api/recruits", params={"q": "語り部 存在しない語"}).json()["total"] == 0


def test_keyword_combines_with_other_conditions_and_paging(client):
    only_regular = client.get("/api/recruits", params={"q": "語り部", "employment": 1}).json()
    assert all("常勤" in i["employments"] for i in only_regular["items"])
    assert only_regular["total"] <= client.get("/api/recruits", params={"q": "語り部"}).json()["total"]
    paged = client.get("/api/recruits", params={"q": "語り部", "per_page": 1}).json()
    assert (paged["total"], paged["last_page"], len(paged["items"])) == (2, 2, 1)


def test_keyword_never_shows_hidden_recruits(client):
    assert client.get("/api/recruits", params={"q": "非表示"}).json()["total"] == 0


def test_keyword_wildcards_are_plain_characters(client):
    for q in ("%", "_", "\\", "語り部%", "語_部"):
        assert client.get("/api/recruits", params={"q": q}).json()["total"] == 0, q


def test_blank_keyword_is_no_filter(client):
    for q in ("", "   ", "　"):
        assert client.get("/api/recruits", params={"q": q}).json()["total"] == 11, repr(q)


def test_too_long_keyword_is_rejected(client):
    assert client.get("/api/recruits", params={"q": "あ" * 100}).status_code == 200
    assert client.get("/api/recruits", params={"q": "あ" * 101}).status_code == 422
    assert client.get("/api/facets", params={"q": "あ" * 101}).status_code == 422


def test_keyword_terms_are_deduplicated_and_capped():
    from app.recruits import _keyword_terms

    assert _keyword_terms("a b a　c d e f") == ["a", "b", "c", "d", "e"]
    assert _keyword_terms(None) == [] and _keyword_terms("  ") == []


def test_facets_apply_the_keyword_to_every_count(client):
    body = client.get("/api/facets", params={"q": "語り部"}).json()
    assert body["total"] == 2
    assert sum(_counts(body["employments"]).values()) >= 2
    # 自分の種類（雇用形態）の選択は件数に効かないが、キーワードは効く
    narrowed = client.get("/api/facets", params={"q": "語り部", "employment": 1}).json()
    assert narrowed["total"] <= 2
    assert sum(_counts(narrowed["employments"]).values()) == sum(_counts(body["employments"]).values())


def test_search_view_plan_cost_stays_far_below_the_jit_threshold(client):
    """検索用ビューの見積もりコストが、JIT の閾値（jit_above_cost の既定 100000）を大きく下回る。

    4 つの中間表を LEFT JOIN して束ねる形は、統計が無いと、行が掛け算になると見積もられ（26 件で約 8.4 億行・コスト約 380 万）、
    検索のたびに JIT のコンパイルが走って、1 本が 35〜140ms かかった。その形に戻したら、このテストが落ちる。
    """
    from app.db import connection

    with connection() as conn:
        plan = conn.execute("EXPLAIN (FORMAT JSON) SELECT count(*) FROM recruit_search s WHERE s.is_displayed").fetchone()["QUERY PLAN"]
    assert plan[0]["Plan"]["Total Cost"] < 10_000


# --- 0009: キーワードの正規化（半角カナ・全角英数字・大文字小文字・ひらがな/カタカナ）。テスト用 DB の求人は seed_demo の 12 件（表示 11 件） ---
def _keyword_total(client, q, **params):
    return client.get("/api/recruits", params={"q": q, **params}).json()["total"]


def test_keyword_existing_matches_are_unchanged_by_normalization(client):
    # 正規化の前から当たっていた語は、同じ件数のまま
    assert _keyword_total(client, "温室") == 4  # 職種名・業務内容・法人名（ザーハード砂漠温室組合 2 件）
    assert _keyword_total(client, "ガイド") == 1  # 夜の花見ガイド
    assert _keyword_total(client, "ミルフェン") == 2  # ミルフェン月面植物園の表示中の 2 件
    assert _keyword_total(client, "語り部") == 2


def test_keyword_ignores_halfwidth_kana_and_hiragana_katakana_differences(client):
    for q in ("ｶﾞｲﾄﾞ", "がいど", "ガいど", "ガイド"):  # 半角カナ、ひらがな、混ぜ書き
        assert _keyword_total(client, q) == 1, q
    for q in ("ﾐﾙﾌｪﾝ", "みるふぇん", "ミルフェン"):
        assert _keyword_total(client, q) == 2, q


def test_keyword_does_not_reach_kanji_readings(client):
    # 読みの辞書は持たない（既知の限界）。おんしつ は 温室 には当たらない
    assert _keyword_total(client, "おんしつ") == 0


def test_keyword_ignores_fullwidth_and_halfwidth_symbols(client):
    # 住所は「（架空の住所）」と全角の括弧で入っている。半角で打っても当たる
    assert _keyword_total(client, "（架空の住所）") == 11
    assert _keyword_total(client, "(架空の住所)") == 11


def test_keyword_ignores_case_and_fullwidth_alphanumerics(client):
    # ダミーデータに英数字を含む求人が無いので、1 件足して確かめる（終わったら消す）
    db_exec("INSERT INTO recruits (category, corporate, is_displayed) VALUES ('Ｗｅｂ編集のＺＺＺ', '正規化テスト法人', true)")
    try:
        for q in ("zzz", "ZZZ", "ＺＺＺ", "ｚｚｚ", "web", "ＷＥＢ", "ｗeb"):
            assert _keyword_total(client, q) == 1, q
    finally:
        db_exec("DELETE FROM recruits WHERE corporate = '正規化テスト法人'")


def test_keyword_terms_in_different_spellings_are_anded(client):
    assert _keyword_total(client, "ｶﾞｲﾄﾞ ミルフェン") == 1
    assert _keyword_total(client, "ｶﾞｲﾄﾞ　みるふぇん") == 1  # 全角の空白でも区切る
    assert _keyword_total(client, "ｶﾞｲﾄﾞ ザーハード") == 0


def test_keyword_wildcards_stay_plain_after_normalization(client):
    # 全角の ％ ＿ ＼ は、正規化で % _ \ になる。エスケープは正規化のあとなので、ワイルドカードにはならない
    for q in ("％", "＿", "＼", "語り部％", "語＿部"):
        assert _keyword_total(client, q) == 0, q


@pytest.mark.parametrize(
    ("raw", "folded"),
    [
        ("ｶﾞｲﾄﾞ", "ガイド"),  # 半角カナ（濁点つき）
        ("ﾐﾙﾌｪﾝ", "ミルフェン"),
        ("がいど", "ガイド"),  # ひらがな
        ("ゔゝ", "ヴヽ"),  # ひらがなの端（ゔ・繰り返し記号）
        ("が", "ガ"),  # 結合文字の濁点
        ("ＺＺＺ１２３", "zzz123"),  # 全角英数字と大文字
        ("Ｓｔａｒ　Ｇａｒｄｅｎ", "star garden"),  # 全角の空白
        ("㈱", "(株)"),
        ("温室", "温室"),  # 漢字は変えない
        ("ー", "ー"),  # 長音は変えない
        ("", ""),
    ],
)
def test_search_fold_rule(client, raw, folded):
    assert db_exec("SELECT search_fold(%s)", (raw,))[0][0] == folded


@pytest.mark.parametrize(
    ("raw", "pattern"),
    [("ガイド", "%ガイド%"), ("ｶﾞｲﾄﾞ", "%ガイド%"), ("100%", "%100\\%%"), ("a_b", "%a\\_b%"), ("\\", "%\\\\%"), ("％＿＼", "%\\%\\_\\\\%")],
)
def test_search_pattern_escapes_after_folding(client, raw, pattern):
    assert db_exec("SELECT search_pattern(%s)", (raw,))[0][0] == pattern


def test_search_view_keeps_the_old_columns_and_adds_search_text_last(client):
    cols = [r[0] for r in db_exec("SELECT column_name FROM information_schema.columns WHERE table_name = 'recruit_search' ORDER BY ordinal_position")]
    assert cols[-5:] == ["area_codes", "station_codes", "occupation_codes", "employment_codes", "search_text"]
    assert len(cols) == 42  # 0008 の 41 列 + search_text


def test_search_text_is_kept_up_to_date_by_the_database(client):
    # 保存した列（生成列）は、書き込みのたびに DB が計算し直す。直接は書けない
    db_exec("INSERT INTO recruits (category, corporate, is_displayed) VALUES ('ｶﾞｲﾄﾞ ＡＢＣ', '正規化テスト法人', false)")
    try:
        assert db_exec("SELECT search_text FROM recruits WHERE corporate = '正規化テスト法人'")[0][0] == "ガイド abc 正規化テスト法人"
        db_exec("UPDATE recruits SET category = 'がいど' WHERE corporate = '正規化テスト法人'")
        assert db_exec("SELECT search_text FROM recruits WHERE corporate = '正規化テスト法人'")[0][0] == "ガイド 正規化テスト法人"
        with pytest.raises(psycopg.errors.GeneratedAlways):
            db_exec("UPDATE recruits SET search_text = 'x' WHERE corporate = '正規化テスト法人'")
    finally:
        db_exec("DELETE FROM recruits WHERE corporate = '正規化テスト法人'")
