"""検索エンジン（Meilisearch）の経路。偽のエンジン（tests/fake_meili.py）で、こちらの呼び方・並び・退避・索引の作り直しを確かめる。

前提: テスト用 DB の求人は seed_demo の 12 件（表示 11 件、非表示 1 件）。既定（SEARCH_ENGINE 未設定）は Postgres の検索で、エンジンを呼ばない。
"""

import pytest

from app import db, search_engine, search_index
from app.settings import meilisearch_config, search_engine_name
from tests.fake_meili import API_KEY, FakeMeili
from tests.test_garden import db_exec


@pytest.fixture(autouse=True)
def _fresh_cooldown():
    """エンジンに届かなかったあとの「聞かない時間」は、テストをまたがせない。"""
    search_engine.reset_cooldown()
    yield
    search_engine.reset_cooldown()


@pytest.fixture
def meili(monkeypatch):
    fake = FakeMeili()
    fake.start()
    monkeypatch.setenv("MEILI_URL", fake.url)
    monkeypatch.setenv("MEILI_API_KEY", API_KEY)
    yield fake
    fake.stop()


@pytest.fixture
def engine_on(monkeypatch, meili):
    """エンジンを使う設定。偽のエンジンには、索引があることにする（索引が無いと、本物と同じく 404 で、Postgres に戻る）。"""
    monkeypatch.setenv("SEARCH_ENGINE", "meilisearch")
    meili.indexes["recruits"] = {"settings": {}, "docs": {}}


def _page(client, **params):
    return client.get("/api/recruits", params={"per_page": 50, **params}).json()


def _displayed_ids(client) -> list[int]:
    return [item["id"] for item in _page(client)["items"]]


def _searches(meili: FakeMeili) -> list[dict]:
    return [r for r in meili.requests if r["path"].endswith("/search")]


def test_default_engine_is_postgres_and_never_calls_the_engine(client, meili):
    assert _page(client, q="温室")["total"] == 4
    assert meili.requests == []


def test_engine_order_is_kept_and_hidden_recruits_never_appear(client, engine_on, meili):
    ids = _displayed_ids(client)
    hidden = db_exec("SELECT id FROM recruits WHERE NOT is_displayed")[0][0]
    meili.search_ids = [ids[5], hidden, ids[2], ids[9]]  # エンジンが、古い索引のために、非表示の求人も返したとする
    body = _page(client, q="温室")
    assert [item["id"] for item in body["items"]] == [ids[5], ids[2], ids[9]]  # 関連度の順（候補の並び）のまま。非表示は SQL が落とす
    assert body["total"] == 3


def test_terms_are_folded_by_the_database_before_they_reach_the_engine(client, engine_on, meili):
    meili.search_ids = []
    body = _page(client, q="ｶﾞｲﾄﾞ　ＺＺＺ")
    assert body["total"] == 0 and body["items"] == []  # 候補が無ければ、LIKE に戻らず 0 件
    (request,) = _searches(meili)
    assert request["body"]["q"] == "ガイド zzz"
    assert request["body"]["matchingStrategy"] == "all"
    assert request["body"]["locales"] == ["jpn"]  # 言語の自動判定に任せない（漢字だけの語が中国語と判定されるため）
    assert request["auth"] == f"Bearer {API_KEY}"


def test_filters_still_apply_to_engine_candidates(client, engine_on, meili):
    everything = _displayed_ids(client)
    meili.search_ids = everything
    narrowed = _page(client, q="温室", area=[3, 4])
    meili.search_ids = None
    assert {i["corporate"] for i in narrowed["items"]} == {"環軌道植物公社", "星庭財団"}  # test_recruits の既存の期待と同じ
    assert narrowed["total"] == len(narrowed["items"])


def test_facets_use_the_same_candidates(client, engine_on, meili):
    ids = _displayed_ids(client)
    chosen = ids[-2:]  # 「温室」を含む求人とは限らない 2 件。LIKE の結果と違えば、経路の取り違えが分かる
    meili.search_ids = chosen
    body = client.get("/api/facets", params={"q": "温室"}).json()
    assert body["total"] == 2
    expected = dict(db_exec("SELECT c, count(*) FROM recruit_search, unnest(area_codes) AS c WHERE id = ANY(%s) GROUP BY c", (chosen,)))
    assert {row["code"]: row["count"] for row in body["areas"]} == expected  # 条件ごとの件数も、同じ候補から数える
    assert len(_searches(meili)) == 1  # 件数の数え方ごとに、引き直さない


def test_no_keyword_does_not_call_the_engine(client, engine_on, meili):
    assert _page(client)["total"] == 11
    assert _searches(meili) == []


def test_falls_back_to_postgres_when_the_engine_is_unreachable(client, engine_on, monkeypatch):
    monkeypatch.setenv("MEILI_URL", "http://127.0.0.1:1")  # 何も待ち受けていない
    assert _page(client, q="温室")["total"] == 4  # Postgres の LIKE の結果と同じ


def test_falls_back_to_postgres_when_the_engine_errors(client, engine_on, meili):
    meili.fail_search = True
    assert _page(client, q="温室")["total"] == 4


def test_falls_back_to_postgres_when_the_engine_rejects_the_key(client, engine_on, meili, monkeypatch):
    monkeypatch.setenv("MEILI_API_KEY", "wrong-key")
    assert _page(client, q="温室")["total"] == 4


def test_falls_back_to_postgres_when_the_result_may_be_truncated(client, engine_on, meili):
    meili.search_ids = list(range(search_engine.SEARCH_LIMIT))  # 上限まで返ってきた = 取りこぼしの恐れ
    assert _page(client, q="温室")["total"] == 4


def test_no_database_connection_is_held_while_waiting_for_the_engine(client, engine_on, meili, monkeypatch):
    seen: list[dict] = []

    def slow_engine(query: str) -> list[int]:
        seen.append(db._pool.get_stats())  # エンジンの返事を待っている間の、接続プールの状態
        return []

    monkeypatch.setattr(search_engine, "search_ids", slow_engine)
    assert _page(client, q="温室")["total"] == 0
    assert client.get("/api/facets", params={"q": "温室"}).json()["total"] == 0
    assert len(seen) == 2
    assert all(s["pool_available"] == s["pool_size"] for s in seen)  # 全部の接続が空いている（1 本も持っていない）


def test_after_a_failure_the_engine_is_not_asked_for_a_while(client, engine_on, meili):
    meili.fail_search = True
    assert _page(client, q="温室")["total"] == 4
    assert _page(client, q="温室")["total"] == 4
    assert len(_searches(meili)) == 1  # 2 回目は、聞かずに Postgres で探した（毎回タイムアウトを待たない）
    search_engine.reset_cooldown()
    meili.fail_search = False
    meili.search_ids = [_displayed_ids(client)[0]]
    assert _page(client, q="温室")["total"] == 1  # 聞く時間が過ぎれば、エンジンに戻る


def test_a_too_broad_query_does_not_stop_asking_the_engine(client, engine_on, meili):
    meili.search_ids = list(range(search_engine.SEARCH_LIMIT))
    assert _page(client, q="温室")["total"] == 4
    assert _page(client, q="温室")["total"] == 4
    assert len(_searches(meili)) == 2  # エンジンは動いている。広すぎた語だけ Postgres に任せ、次も聞く


def test_a_broken_http_response_falls_back_to_postgres(client, engine_on, meili):
    meili.short_body = True  # IncompleteRead（OSError ではない）
    assert _page(client, q="温室")["total"] == 4


def test_redirects_are_not_followed_so_the_key_is_not_sent_elsewhere(client, engine_on, meili):
    other = FakeMeili()
    other.start()
    try:
        meili.redirect_to = other.url + "/indexes/recruits/search"
        assert _page(client, q="温室")["total"] == 4
        assert other.requests == []  # リダイレクト先には、何も（Authorization も）届かない
    finally:
        other.stop()


@pytest.mark.parametrize("q", ["-温室", '"温室"', "-", "温室 -ガイド", "－温室"])
def test_search_syntax_is_left_to_postgres(client, engine_on, meili, q):
    # Meilisearch の検索構文（語頭の - は除外、" は句）は、Postgres の LIKE ではただの文字。経路で意味が変わらないよう、エンジンに渡さない
    meili.search_ids = _displayed_ids(client)
    assert _page(client, q=q)["total"] == 0
    assert _searches(meili) == []


@pytest.mark.parametrize("q", ["！", "・", "...", "温室 ・", "（", "…"])
def test_terms_without_letters_are_left_to_postgres(client, engine_on, meili, monkeypatch, q):
    # エンジンは、語が 1 つも無いクエリを全件の検索として扱う。経路で件数が変わらないよう、文字も数字も無い語はエンジンに渡さない
    meili.search_ids = _displayed_ids(client)
    monkeypatch.setenv("SEARCH_ENGINE", "postgres")
    expected = _page(client, q=q)["total"]
    monkeypatch.setenv("SEARCH_ENGINE", "meilisearch")
    assert _page(client, q=q)["total"] == expected
    assert _searches(meili) == []


def test_too_many_waiting_requests_fall_back_instead_of_piling_up(client, engine_on, meili):
    held = [search_engine._slots.acquire(blocking=False) for _ in range(search_engine.MAX_CONCURRENT)]
    try:
        assert all(held)
        assert _page(client, q="温室")["total"] == 4  # エンジンを待っている数が上限なら、待たずに Postgres で探す
        assert _searches(meili) == []
    finally:
        for _ in held:
            search_engine._slots.release()
    meili.search_ids = [_displayed_ids(client)[0]]
    assert _page(client, q="温室")["total"] == 1  # 空けば、エンジンに戻る（聞かない時間も作らない）


def test_the_indexed_columns_match_what_search_text_is_made_of(client):
    # 索引に入れる列（SEARCHABLE）と、DB の recruit_search_text の対象の列が、同じであること（片方だけ変えると、検索の結果が経路で食い違う）
    columns = ", ".join(search_engine.SEARCHABLE)
    differing = db_exec(f"SELECT count(*) FROM recruits WHERE search_text IS DISTINCT FROM search_fold(concat_ws(' ', {columns}))")  # noqa: S608
    assert differing[0][0] == 0


def test_first_build_fills_a_staging_index_before_it_gets_the_live_name(client, meili):
    assert search_index.main([]) == 0
    creates = [r["body"]["uid"] for r in meili.requests if (r["method"], r["path"]) == ("POST", "/indexes")]
    assert creates == ["recruits_next"]  # 使う名前の索引は、直接は作らない（空・一部だけの索引が、使われる時間を作らない）
    assert ("PATCH", "/indexes/recruits_next") in [(r["method"], r["path"]) for r in meili.requests]  # 全件が入ってから改名
    assert set(meili.indexes) == {"recruits"} and len(meili.indexes["recruits"]["docs"]) == 11


def test_a_failed_first_build_leaves_no_live_index(client, meili, capsys):
    meili.fail_documents = True
    assert search_index.main([]) == 1
    assert "recruits" not in meili.indexes  # 一部だけの索引は、使う名前では残らない（Postgres の検索に退避できる）
    meili.fail_documents = False
    assert search_index.main([]) == 0  # 前回の途中の残り（recruits_next）は、片づけてから作り直す
    assert set(meili.indexes) == {"recruits"}


def test_reindex_builds_the_index_from_displayed_recruits_in_folded_form(client, meili):
    assert search_index.main([]) == 0
    index = meili.indexes["recruits"]
    assert set(index["docs"]) == set(_displayed_ids(client))  # 表示中だけ（非表示の文を、エンジンに置かない）
    assert index["settings"]["searchableAttributes"] == list(search_engine.SEARCHABLE)
    assert index["settings"]["displayedAttributes"] == ["id"]
    assert index["settings"]["localizedAttributes"] == [{"locales": ["jpn"], "attributePatterns": ["*"]}]
    some_id = next(iter(index["docs"]))
    folded = db_exec("SELECT search_fold(category), search_fold(corporate) FROM recruits WHERE id = %s", (some_id,))[0]
    assert (index["docs"][some_id]["category"], index["docs"][some_id]["corporate"]) == tuple(folded)  # DB の関数を通した形


def test_reindex_swaps_instead_of_emptying_when_the_index_exists(client, meili):
    assert search_index.main([]) == 0
    meili.requests.clear()
    assert search_index.main([]) == 0
    calls = [(r["method"], r["path"]) for r in meili.requests]
    assert ("POST", "/swap-indexes") in calls
    assert ("DELETE", "/indexes/recruits") not in calls  # 使っている索引は消さない（空の時間を作らない）
    assert ("DELETE", "/indexes/recruits_next") in calls  # 入れ替えのあと、古い内容の側を消す
    assert set(meili.indexes) == {"recruits"}


def test_reindex_status_reports_missing_and_present_index(client, meili, capsys):
    assert search_index.main(["--status"]) == 0
    assert "なし" in capsys.readouterr().out
    assert search_index.main([]) == 0
    capsys.readouterr()
    assert search_index.main(["--status"]) == 0
    assert "あり（11 件）" in capsys.readouterr().out


def test_reindex_reports_an_unreachable_engine_without_leaking_the_url(client, monkeypatch, capsys):
    monkeypatch.setenv("MEILI_URL", "http://127.0.0.1:1")
    assert search_index.main([]) == 1
    out = capsys.readouterr().out
    assert "届きません" in out and "127.0.0.1" not in out


def test_settings_reject_bad_values_instead_of_switching(monkeypatch):
    monkeypatch.setenv("SEARCH_ENGINE", "elasticsearch")
    with pytest.raises(RuntimeError, match="SEARCH_ENGINE"):
        search_engine_name()
    monkeypatch.delenv("MEILI_URL", raising=False)
    with pytest.raises(RuntimeError, match="MEILI_URL"):
        meilisearch_config()
    monkeypatch.setenv("MEILI_URL", "ftp://example.com")
    with pytest.raises(RuntimeError, match="http"):
        meilisearch_config()
    monkeypatch.setenv("MEILI_URL", "http://meilisearch:7700/")
    for bad_key in ("日本語のキー", "line1\nline2"):  # HTTP のヘッダーに入れられない文字は、起動で止める（検索のたびの 500 にしない）
        monkeypatch.setenv("MEILI_API_KEY", bad_key)
        with pytest.raises(RuntimeError, match="MEILI_API_KEY") as error:
            meilisearch_config()
        assert bad_key not in str(error.value)
    monkeypatch.delenv("MEILI_API_KEY")
    monkeypatch.setenv("MEILI_TIMEOUT_SECONDS", "0")
    with pytest.raises(RuntimeError, match="MEILI_TIMEOUT_SECONDS"):
        meilisearch_config()


def test_default_settings():
    assert search_engine_name() == "postgres"
