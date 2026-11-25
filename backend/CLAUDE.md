# backend/（API）

全体の方針・禁止事項は、ルートの [CLAUDE.md](../CLAUDE.md)。置き換えの経緯・範囲・確かめ方は [docs/go-migration.md](../docs/go-migration.md)。画像・マイグレーション・バックアップの詳細は [docs/image-storage.md](../docs/image-storage.md)。

Go 1.26（`go.mod`）。標準ライブラリ + `pgx`（生の SQL。ORM・マイグレーションの道具は使わない）。1 つの `package main` に平らに置く。ビルドは **`-tags nodynamic`**（WebP は共有ライブラリを探さず、組み込みの libwebp だけを使う）。依存は `go.mod`・`go.sum`（直接編集しない。`go mod tidy`）。新しい依存は、先に確認する。

## ファイル

[docs/go-migration.md](../docs/go-migration.md) の「構成」の表。1 つのバイナリ `api` が、引数なしか `serve` で API、ほかはサブコマンド（`migrate` `seed` `seed-images` `images` `search-index` `healthcheck`）。

## 守ること

- **SQL の値は必ずパラメーターで渡す。** 文字列でつなげてよいのは、固定の断片（表名・条件の骨組み）だけ。pgx は Exec モード（`QueryExecModeExec`）なので、整数の引数は `int8` で渡る。型が曖昧な場所（`make_interval(mins => …)`・`pg_advisory_xact_lock`）は `$1::int` のように型を付ける。
- **画像の登録は `registerImage`（`image_service.go`）を通す。** 園のアップロード、運営の CLI、seed が同じ検証・登録を使う。別の経路で `images` に書かない。保存キーは `images/<uuid>.<拡張子>`。同じキーへの別の内容の上書きはしない。署名付き URL は作らない。
- **先に DB へ意図を書き、そのあとストレージを触る。** 削除は先に DB を確定してから実体を消す。この順を変えない（`image_service.go` の冒頭）。**法人の行（`garden_accounts`）の `FOR UPDATE` を持ったまま画像を登録しない**（`images` の外部キーが待たされて止まる）。
- **マイグレーション:** `migrations/NNNN_名前.sql` を新しい番号で足すだけ。適用済みを書き換えない・`DROP` / `TRUNCATE` を入れない。`db/schema.sql` を変えたら、`baseline_fingerprint.json` と `cli_test.go` の `TestMigrate*` の整合を確かめる。SQL はバイナリに埋め込む（`go:embed`）ので、足したら再ビルドする。
- **検索用ビュー `recruit_search` は、中間表を JOIN して束ねない**（配列は `ARRAY(SELECT …)`。0008）。**キーワード検索の正規化は DB の関数 1 か所**（`search_fold`・`recruit_search_text`。0009。関数の中から別の関数を呼ぶときは `public.` を付ける。`TestBackupAndRestoreRoundtrip` が見張る）。検索エンジン（Meilisearch）は写しで、返すのは id だけ。表示中か・条件・権限は、いつも SQL が判定する。索引に入れる列（`engineSearchable`）は `recruit_search_text` の対象の列と同じにする。
- **エラー本文は固定の文。** 例外の文字列・パス・接続先を応答にも、ログにも出さない（`s.dbFailure` は型だけをログに出す）。認証の失敗は、メールの有無が応答の速さや文面から分からないようにする。
- **応答の形（項目の順・エラーの文・422・405・CORS）は、旧 Python の API と同じ。** 変えるときは `testdata/golden_read.json` を作り直し、画面（`frontend/src/lib/types.ts`）への影響を確かめる。
- **権限の確認は SQL の条件で行う。** 画面を信用しない。他の園のものは 404。求職者の ID は、検証に通った JWT の `sub`（`users.auth_user_id`）だけから取る。署名は JWKS の公開鍵（ES256 / RS256）だけで検証し、共有秘密や `alg=none` へは切り替えない（`jwt.go`）。
- `DEMO_DATA`、`STORAGE_S3_*` などの認証情報は、環境変数だけで受け取る。ログ・例外に出さない。

## テスト・実行

```bash
cd backend
TEST_DATABASE_URL=postgresql://postgres@127.0.0.1:55432/postgres go test -tags nodynamic -race -count=1 ./...
```

- **`TEST_DATABASE_URL`（Postgres 17）が無いと DB のテスト（ほぼ全部）はスキップされる。** スキップのまま通っても、検証したことにならない。`TestBackupAndRestoreRoundtrip` は `/usr/lib/postgresql/17/bin/pg_dump` も要る（無いとスキップ。CI は入れて、スキップ 0 を確かめる）。
- テストは、テンプレートの DB（`jobnavi_go_template`）を作り、テストごとに複製した DB（`jobnavi_go_t_*`）を使う。**同じサーバーの別の DB を DROP するので、`TEST_DATABASE_URL` は必ずテスト用のサーバーに向ける。**
- 外部のサービスは呼ばない。S3 は偽のサーバー（`storage_test.go`）、Meilisearch は偽のサーバー（`search_test.go`）、JWKS は偽のサーバー（`testutil_test.go`）。画像はテスト用に作る。
- 新しいテストは、対象のファイル名に合わせた `<対象>_test.go` に足す。
- ホストに Go が無いときは、`golang:1.26.10` のコンテナで流せる（`docker run --rm -v "$PWD/..":/repo -w /repo/backend golang:1.26.10 go test …`）。
