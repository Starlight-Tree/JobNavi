"""検索エンジン（Meilisearch）の索引を、DB から作り直す。DB が正で、索引はその写し（app/search_engine.py）。

  python -m app.search_index            表示中の求人を、索引に入れ直す（何度流しても同じ。入れ替えの間も検索できる）
  python -m app.search_index --status  届くか・索引があるか・文書の数

必要な環境変数: DATABASE_URL、MEILI_URL（API キーは MEILI_API_KEY）。求人を足す・変える・非表示にしたあと（seed_demo のあとを含む）に流す。
流し忘れても、非表示の求人は出ない（表示中かは SQL が決める）。新しい求人が検索に出ないだけ。
"""

import sys

import psycopg
from psycopg.rows import dict_row

from app import search_engine
from app.settings import database_url, meilisearch_config


def main(argv: list[str]) -> int:
    try:
        meilisearch_config()
        if "--status" in argv:
            info = search_engine.status()
            print("検索エンジン: 届きます")
            print("索引:", f"あり（{info['documents']} 件）" if info["index"] else "なし（python -m app.search_index で作る）")
            return 0
        with psycopg.connect(database_url(), row_factory=dict_row) as conn:
            search_engine.reindex(conn)
        return 0
    except search_engine.EngineUnavailable as exc:
        print(f"失敗しました。{exc}")
        return 1
    except psycopg.Error as exc:
        # 接続情報を含みうる例外の本文は出さず、種類だけ出す
        print(f"失敗しました（{type(exc).__name__}）。DB への接続か SQL の実行でエラーです")
        return 1
    except RuntimeError as exc:  # 環境変数の設定ミス（値は含まない固定の文）
        print(f"失敗しました。{exc}")
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
