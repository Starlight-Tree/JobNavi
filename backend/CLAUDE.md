# backend/（API）

全体の方針・禁止事項は、ルートの [CLAUDE.md](../CLAUDE.md)。画像・マイグレーション・バックアップの詳細は [docs/image-storage.md](../docs/image-storage.md)。

FastAPI + psycopg3（生の SQL。ORM・Alembic は使わない）。Python 3.13（`Dockerfile`）。依存は `requirements.in` と `requirements-dev.in`、固定版が `requirements.txt`（直接編集しない）。

## モジュール（`app/`）

| ファイル | 役割 |
|---|---|
| `main.py` | アプリの組み立て。ルーターの登録、CORS、`/health`。`/media` は `STORAGE_BACKEND=local` のときだけ |
| `settings.py` | 環境変数の読み取り。設定はここに集める。必須の値が無いときは起動に失敗させ、別の値に切り替えない |
| `db.py` | 接続プール（`dict_row`） |
| `auth.py` / `jwt_auth.py` / `garden.py` / `security.py` | 利用者は、Better Auth（画面のサーバーの中）が出す JWT を、`jwt_auth.py` が JWKS の公開鍵で検証し、`auth.py` が `sub` を `users.auth_user_id` に結んでプロフィールを返す（`GET`・`PATCH /api/auth/me`。はじめての人は最初のリクエストで行を作る）。ログイン自体は API では扱わない。園は、`garden.py` のパスワードとセッション（表も別）。園の部品（トークン・argon2）は `security.py` |
| `recruits.py` / `schemas.py` | 求人の検索・詳細・条件ごとの件数（`/api/facets`）と、応答の型 |
| `images_api.py` / `image_queries.py` / `image_service.py` / `storage.py` | 画像。API、検索用の SQL、検証・登録・取り下げ・点検、保存先（`LocalStorage` / `S3Storage`） |
| `images_cli.py` | 運営用 CLI（`list` `register` `reject` `reconcile` `formfile`）。園の API からは呼べない |
| `migrate.py` / `migrations/` | 番号付き SQL の実行器と、`0002` 以降のファイル。`baseline_fingerprint.json` は 0001 の照合用 |
| `seed_demo.py` / `seed_images.py` / `demo_images.json` | ダミーデータ。`DEMO_DATA=allow` が無いと実行を拒否する |

## 守ること

- **SQL の値は必ずパラメータで渡す。** f-string で組み立ててよいのは、固定の断片（表名・条件の骨組み）だけ（`recruits.py` の `clause`）。
- **画像の登録は `image_service.register_image` を通す。** 園のアップロード、運営の CLI、seed が同じ検証・登録を使う。別の経路で `images` に書かない。
  保存キーは `images/<uuid>.<拡張子>`。同じキーへの別の内容の上書きはしない（更新は新しい画像として登録）。署名付き URL は作らない。
- **先に DB へ意図を書き、そのあとストレージを触る。** 削除は先に DB を確定してから実体を消す。この順を変えない（食い違いの設計は `image_service.py` の冒頭）。
- **マイグレーション:** `migrations/NNNN_名前.sql` を足すだけ。適用済みを書き換えない・`DROP` / `TRUNCATE` を入れない。`db/schema.sql` を変えたら、`baseline_fingerprint.json` と `test_migrate.py` の整合を確かめる。
- **検索用ビュー `recruit_search` は、中間表を JOIN して束ねない。** 配列の列は `ARRAY(SELECT …)` の相関サブクエリで作る（`0008`）。JOIN の掛け算は、統計が無いと見積もりが数億行になり、JIT が毎回走って、1 本 35〜140ms かかる。`tests/test_recruits.py` が、ビューの見積もりコスト（10,000 未満）で見張る。
- **キーワード検索の正規化は、DB の関数 1 か所。** 検索の対象の列と規則は `recruit_search_text` / `search_fold`（`0009`）で、`recruits.search_text`（生成列・保存）を `search_text LIKE search_pattern(語)` で探す。変えるときは新しいマイグレーションで関数を作り直し、`ALTER TABLE recruits ALTER COLUMN search_text SET EXPRESSION AS (…)` で保存値を計算し直す（自動では直らない）。関数の中から別の関数を呼ぶときは `public.` を付ける（`pg_restore` は `search_path` を空にするので、付けないとバックアップから戻せない。`test_backup_and_restore_roundtrip` が見張る）。
- **キーワード検索の候補は、任意で Meilisearch から引ける（`SEARCH_ENGINE=meilisearch`。既定は Postgres）。** Postgres が正で、エンジンは写し（`search_engine.py`）。エンジンが返すのは id だけで、表示中か・条件・権限は、いつも SQL が判定する。エンジンに届かない・索引が無い・結果が多すぎる・検索構文を含む語のときは、Postgres の `LIKE` に退避する。エンジンの返事を待つ間は、DB の接続を持たない。索引に入れる列（`SEARCHABLE`）は `recruit_search_text` の対象の列と同じにする（`test_search_engine.py` が見張る）。索引づくりは `python -m app.search_index`（[docs/development.md](../docs/development.md) の「検索エンジン（任意）」）。
- **エラー本文は固定の文。** 例外の文字列を `detail` に入れない。認証の失敗は、メールの有無が応答の速さや文面から分からないようにする（`security.py`）。
- **園のログインは、メールごとに試行を数えて制限する**（`garden.py`。表は `0007_garden_login_attempts`、鍵はメールの SHA-256 だけ。上限を超えると正しいパスワードでも固定の 429）。IP では数えない。上限・窓は `settings.py` の `garden_login_limits()`。数値と限界は [docs/development.md](../docs/development.md) の「園のログインの試行制限と CORS」。
- **権限の確認は API の SQL の条件で行う。** 画面を信用しない。他の園のものは 404。
- **求職者の ID は、検証に通った JWT の `sub`（`users.auth_user_id`）だけから取る。** 画面・クライアントが送った別の値は使わない。署名は JWKS の公開鍵（ES256 / RS256）だけで検証し、共有秘密（HS256）や `alg=none` へは切り替えない（`jwt_auth.py`）。
- `DEMO_DATA`、`STORAGE_S3_*` などの認証情報は、環境変数だけで受け取る。ログ・例外に出さない。
- 新しい依存は `requirements.in` に足して `requirements.txt` を作り直す（`requirements.txt` の 1 行目に、uv が使える環境では `uv pip compile` で作り直してよいとある）。作り直しは差分が大きくなるので、先に確認する。

## テスト（`tests/`）

- `pytest`（`pytest.ini`: `testpaths = tests`、`pythonpath = .`）。**`TEST_DATABASE_URL`（Postgres 17）が無いと DB のテストはスキップされる。** スキップのまま通っても、検証したことにならない。
- `conftest.py` の `client` は、セッション単位で `hj_test` を作り直し、`db/schema.sql` と `seed_codes.sql` → `migrate` → `seed_demo --reset` を流す。マイグレーションのテストは、別名の DB を `new_database` で作る。
- 外部のサービスは呼ばない。S3 は `moto`。画像はテスト用に作る（`imagefiles.py`）。保存先はテスト用の一時フォルダー。
- 新しいテストは、対象のモジュール名に合わせた `test_<対象>.py` に足す。補助モジュールは `test_` で始めない。

## 実行

```bash
cd backend
python -m venv .venv && .venv/bin/pip install -r requirements.txt -r requirements-dev.in
TEST_DATABASE_URL=postgresql://postgres@127.0.0.1:55432/postgres .venv/bin/python -m pytest -q
```

lint・整形（ruff など）は未導入。導入するときは、別の提案として承認を得る。
