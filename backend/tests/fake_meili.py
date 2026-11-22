"""テスト用の偽の Meilisearch（標準ライブラリだけの HTTP サーバー）。app/search_engine.py が使う口だけを持つ。

本物のエンジンの検索の質は確かめない（ここで確かめるのは、こちらの呼び方・並びの使い方・退避・索引の作り直しの流れ）。
書き込みは、すぐ成功するタスクとして返す。受けた要求は requests に残る（メソッド・パス・本文・Authorization）。
"""

import json
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

API_KEY = "test-key"


class FakeMeili:
    def __init__(self) -> None:
        self.indexes: dict[str, dict[str, Any]] = {}
        self.requests: list[dict[str, Any]] = []
        # 検索で返す id（None なら、語をすべて含む文書を、id の大きい順に返す）。順番はエンジンが決めることの代わり
        self.search_ids: list[int] | None = None
        self.fail_search = False
        self.short_body = False  # 検索の応答を、Content-Length より短い本文で切る（エンジンが途中で落ちた・プロキシが切った）
        self.redirect_to: str | None = None  # 検索の応答を、この URL への 302 にする
        self.fail_documents = False  # 文書を足すタスクを、失敗にする（索引づくりの途中失敗）
        self._failed: set[int] = set()
        self._tasks = 0
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args: object) -> None:
                pass

            def _send(self, status: int, body: Any = None) -> None:
                raw = b"" if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def _handle(self, method: str) -> None:
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                body = json.loads(raw) if raw else None
                path = self.path.split("?", 1)[0]
                auth = self.headers.get("Authorization")
                owner.requests.append({"method": method, "path": path, "body": body, "auth": auth})
                if path != "/health" and auth != f"Bearer {API_KEY}":
                    return self._send(401, {"code": "invalid_api_key"})
                if path.endswith("/search") and owner.redirect_to:
                    self.send_response(302)
                    self.send_header("Location", owner.redirect_to)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                if path.endswith("/search") and owner.short_body:
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", "500")
                    self.end_headers()
                    self.wfile.write(b'{"hits": [')
                    self.close_connection = True
                    return
                status, payload = owner._route(method, path, body)
                self._send(status, payload)

            def do_GET(self) -> None:
                self._handle("GET")

            def do_POST(self) -> None:
                self._handle("POST")

            def do_PATCH(self) -> None:
                self._handle("PATCH")

            def do_DELETE(self) -> None:
                self._handle("DELETE")

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self._server.server_address[1]}"
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._server.shutdown()
        self._server.server_close()

    def _task(self) -> tuple[int, dict]:
        self._tasks += 1
        return 202, {"taskUid": self._tasks, "status": "enqueued"}

    def _route(self, method: str, path: str, body: Any) -> tuple[int, Any]:
        if (method, path) == ("GET", "/health"):
            return 200, {"status": "available"}
        if (method, path) == ("POST", "/indexes"):
            self.indexes[body["uid"]] = {"settings": {}, "docs": {}}
            return self._task()
        if (method, path) == ("POST", "/swap-indexes"):
            first, second = body[0]["indexes"]
            self.indexes[first], self.indexes[second] = self.indexes[second], self.indexes[first]
            return self._task()
        if m := re.fullmatch(r"/tasks/(\d+)", path):
            uid = int(m.group(1))
            if uid in self._failed:
                return 200, {"uid": uid, "status": "failed", "error": {"code": "internal"}}
            return 200, {"uid": uid, "status": "succeeded"}
        if m := re.fullmatch(r"/indexes/([a-z_]+)(/[a-z]+)?", path):
            uid, tail = m.group(1), m.group(2)
            if uid not in self.indexes:
                return 404, {"code": "index_not_found"}
            index = self.indexes[uid]
            if (method, tail) == ("GET", None):
                return 200, {"uid": uid}
            if (method, tail) == ("DELETE", None):
                del self.indexes[uid]
                return self._task()
            if (method, tail) == ("PATCH", None):  # 改名
                self.indexes[body["uid"]] = self.indexes.pop(uid)
                return self._task()
            if (method, tail) == ("PATCH", "/settings"):
                index["settings"] = body
                return self._task()
            if (method, tail) == ("POST", "/documents"):
                for doc in body:
                    index["docs"][doc["id"]] = doc
                status, task = self._task()
                if self.fail_documents:
                    self._failed.add(task["taskUid"])
                return status, task
            if (method, tail) == ("GET", "/stats"):
                return 200, {"numberOfDocuments": len(index["docs"])}
            if (method, tail) == ("POST", "/search"):
                return self._search(index, body)
        return 404, {"code": "not_found"}

    def _search(self, index: dict, body: dict) -> tuple[int, Any]:
        if self.fail_search:
            return 500, {"code": "internal"}
        if self.search_ids is not None:
            ids = self.search_ids
        else:
            words = body["q"].split()
            ids = sorted(
                (d["id"] for d in index["docs"].values() if all(w in " ".join(str(v) for v in d.values() if v) for w in words)),
                reverse=True,
            )
        return 200, {"hits": [{"id": i} for i in ids[: body["limit"]]], "estimatedTotalHits": len(ids)}
