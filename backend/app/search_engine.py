"""検索エンジン（Meilisearch）。Postgres が正で、ここは写し（索引）。

使うのは、キーワード（q）の「候補の id（関連度の順）」を引くことだけ。表示中か・条件に合うか・権限は、これまでどおり SQL で判定する
（索引が古くても、非表示の求人は出ない。新しい求人が出ないだけ）。索引は `python -m app.search_index` で、DB から作り直す。
検索語と求人の文字列は、同じ DB の関数 search_fold（0009）を通してから渡す（規則を 1 か所にし、半角カナ・全角英数字などは、エンジンに任せない）。

エンジンに届かない・期待した応答でないときは EngineUnavailable。呼び出し側は Postgres の検索に戻る。例外の本文は固定の文（URL・鍵を含めない）。
"""

import http.client
import json
import re
import threading
import time
import urllib.error
import urllib.request
from collections.abc import Callable
from typing import Any

from psycopg import Connection

from app import settings

INDEX_UID = "recruits"
# 検索 1 回で受け取る候補の上限。受け取った数がこれに届いたら、取りこぼしの恐れがあるので使わない（Postgres の検索に戻る）
SEARCH_LIMIT = 1000
LOCALE = "jpn"  # 求人はすべて日本語
# 索引に入れる列。順番が、関連度の重み（先ほど重い）。recruit_search_text（0009）の対象の列と同じ
SEARCHABLE = ("category", "corporate", "profile", "place", "comment1", "comment2", "comment3")
SETTINGS = {
    "searchableAttributes": list(SEARCHABLE),
    "displayedAttributes": ["id"],  # 返すのは id だけ。文は、DB から引く
    "filterableAttributes": [],
    "sortableAttributes": [],
    "pagination": {"maxTotalHits": SEARCH_LIMIT},
    # 言語は日本語と決める。自動判定だと、漢字だけの語（「温室」）が中国語と判定され、日本語として切った文書と食い違って 0 件になる（実機で確認した）
    "localizedAttributes": [{"locales": [LOCALE], "attributePatterns": ["*"]}],
}
BATCH = 1000
TASK_TIMEOUT_SECONDS = 60
# エンジンに届かなかったあと、この秒数は聞かずに Postgres で探す（毎回タイムアウトを待たないため）
COOLDOWN_SECONDS = 10
_down_until = 0.0
# エンジンを同時に待つ数の上限。超えた分は待たずに Postgres で探す（エンジンが固まっても、ワーカーを全部塞がないため）
MAX_CONCURRENT = 4
_slots = threading.BoundedSemaphore(MAX_CONCURRENT)


class EngineUnavailable(Exception):
    """検索エンジンに届かない、または期待した応答でない。"""


class EngineHTTPError(EngineUnavailable):
    def __init__(self, status: int) -> None:
        super().__init__(f"検索エンジンが {status} を返しました")
        self.status = status


class ResultTooLarge(EngineUnavailable):
    """エンジンは動いているが、候補が多すぎて全件を取れない。止まっているわけではないので、聞かない時間（COOLDOWN）は作らない。"""


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args: object, **kwargs: object) -> None:
        return None  # 3xx は辿らない（Authorization を、別のホストへ送らないため）。HTTPError になる


_opener = urllib.request.build_opener(_NoRedirect)


def reset_cooldown() -> None:
    global _down_until
    _down_until = 0.0


def _call(method: str, path: str, body: Any = None) -> Any:
    url, key, timeout = settings.meilisearch_config()
    data = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(url + path, data=data, method=method)  # noqa: S310 URL は http(s) だけ（settings が検査する）
    request.add_header("Content-Type", "application/json")
    if key:
        request.add_header("Authorization", f"Bearer {key}")
    try:
        with _opener.open(request, timeout=timeout) as response:
            raw = response.read()
    except urllib.error.HTTPError as exc:
        raise EngineHTTPError(exc.code) from None
    except (urllib.error.URLError, TimeoutError, OSError, http.client.HTTPException, ValueError):  # 応答が途中で切れる・HTTP でない応答・不正なヘッダー値も、届かないのと同じ
        raise EngineUnavailable("検索エンジンに届きません") from None
    try:
        return json.loads(raw) if raw else None
    except ValueError:
        raise EngineUnavailable("検索エンジンの応答を読めません") from None


def usable_terms(folded_terms: list[str]) -> bool:
    """畳んだ語を、そのままエンジンに渡してよいか。Meilisearch の検索構文（語頭の - は除外、" は句）を含む語・空の語・文字も数字も無い語（「！」「・」など。
    エンジンは語が 1 つも無いクエリを、全件の検索として扱う）は渡さない（Postgres の LIKE では、ただの文字なので、経路によって意味が変わる。Postgres で探す）。"""
    return all(t and not t.startswith("-") and '"' not in t and re.search(r"\w", t) for t in folded_terms)


def search_ids(query: str) -> list[int]:
    """畳んだ検索語（語は空白で区切る。すべての語を含む求人だけ）に合う求人の id を、関連度の順で返す。

    届かない・エラー・応答が不正なときは、COOLDOWN_SECONDS の間、聞かずに EngineUnavailable にする（呼び出し側は Postgres で探す）。
    """
    global _down_until
    if time.monotonic() < _down_until:
        raise EngineUnavailable("検索エンジンを一時的に使っていません")
    if not _slots.acquire(blocking=False):
        raise EngineUnavailable("検索エンジンが混み合っています")  # 止まっているとは限らないので、聞かない時間は作らない
    try:
        return _search(query)
    except ResultTooLarge:
        raise
    except EngineUnavailable:
        _down_until = time.monotonic() + COOLDOWN_SECONDS
        raise
    finally:
        _slots.release()


def _search(query: str) -> list[int]:
    result = _call(
        "POST",
        f"/indexes/{INDEX_UID}/search",
        {"q": query, "limit": SEARCH_LIMIT, "matchingStrategy": "all", "attributesToRetrieve": ["id"], "locales": [LOCALE]},
    )
    hits = result.get("hits") if isinstance(result, dict) else None
    if not isinstance(hits, list) or not all(isinstance(h, dict) and isinstance(h.get("id"), int) for h in hits):
        raise EngineUnavailable("検索エンジンの応答を読めません")
    if len(hits) >= SEARCH_LIMIT:
        raise ResultTooLarge("検索結果が多く、全件を取れません")
    return [h["id"] for h in hits]


def _wait(task: Any) -> None:
    """書き込みの処理（タスク）が終わるまで待つ。失敗・時間切れは EngineUnavailable。"""
    uid = task.get("taskUid") if isinstance(task, dict) else None
    if not isinstance(uid, int):
        raise EngineUnavailable("検索エンジンの応答を読めません")
    deadline = time.monotonic() + TASK_TIMEOUT_SECONDS
    delay = 0.05
    while True:
        info = _call("GET", f"/tasks/{uid}")
        status = info.get("status") if isinstance(info, dict) else None
        if status == "succeeded":
            return
        if status in ("failed", "canceled"):
            code = (info.get("error") or {}).get("code", "") if isinstance(info, dict) else ""
            raise EngineUnavailable(f"検索エンジンの処理が失敗しました（{code or status}）")
        if time.monotonic() > deadline:
            raise EngineUnavailable("検索エンジンの処理が時間内に終わりません")
        time.sleep(delay)
        delay = min(delay * 2, 1.0)


def _index_exists(uid: str) -> bool:
    try:
        _call("GET", f"/indexes/{uid}")
    except EngineHTTPError as exc:
        if exc.status == 404:
            return False
        raise
    return True


def _build(uid: str, documents: list[dict]) -> None:
    _wait(_call("POST", "/indexes", {"uid": uid, "primaryKey": "id"}))
    _wait(_call("PATCH", f"/indexes/{uid}/settings", SETTINGS))
    for start in range(0, len(documents), BATCH):
        _wait(_call("POST", f"/indexes/{uid}/documents", documents[start : start + BATCH]))


def reindex(conn: Connection, out: Callable[[str], None] = print) -> int:
    """表示中の求人を、DB から索引に入れ直す。作り直した件数を返す。

    いつも別名の索引（recruits_next）に全件を入れてから、使う名前にする。すでに索引があるときは入れ替え（swap）、無いときは改名する。
    作っている間は、使う名前の索引が「空・一部だけ」になる時間が無い（古い索引で検索できるか、索引が無くて Postgres に退避する）。途中で失敗しても、同じ。
    文は search_fold（0009）を通した形で入れる。索引に入れる列（SEARCHABLE）は、recruit_search_text（0009）の対象の列と同じ。片方を変えたら、もう片方も直す
    （tests/test_search_engine.py が見張る）。
    """
    columns = ", ".join(f"search_fold({c}) AS {c}" for c in SEARCHABLE)  # 列名は、このモジュールの固定の文字列だけ
    documents = conn.execute(f"SELECT id, {columns} FROM recruits WHERE is_displayed ORDER BY id").fetchall()  # noqa: S608
    staging = f"{INDEX_UID}_next"
    if _index_exists(staging):
        _wait(_call("DELETE", f"/indexes/{staging}"))  # 前回の途中の残り
    _build(staging, documents)
    if _index_exists(INDEX_UID):
        _wait(_call("POST", "/swap-indexes", [{"indexes": [INDEX_UID, staging]}]))
        _wait(_call("DELETE", f"/indexes/{staging}"))  # 入れ替えのあと、staging には古い内容がある
    else:
        _wait(_call("PATCH", f"/indexes/{staging}", {"uid": INDEX_UID}))  # 全件が入ってから、使う名前にする
    out(f"{len(documents)} 件を索引に入れました")
    return len(documents)


def status() -> dict[str, Any]:
    """動作の確認用。(届くか・索引があるか・文書の数)。届かなければ EngineUnavailable。"""
    _call("GET", "/health")
    try:
        stats = _call("GET", f"/indexes/{INDEX_UID}/stats")
    except EngineHTTPError as exc:
        if exc.status == 404:
            return {"index": False, "documents": 0}
        raise
    count = stats.get("numberOfDocuments") if isinstance(stats, dict) else None
    return {"index": True, "documents": count if isinstance(count, int) else 0}
