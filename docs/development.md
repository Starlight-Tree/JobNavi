# 開発

README の Quick start の続き。ローカルでの開発・テスト・ダミーデータ・CI の手順。

## 構成と版

| 場所 | 役割 | 版 |
|---|---|---|
| `backend/` | API（Go。標準ライブラリ + pgx。生の SQL。2026-11 に Python から置き換えた: [docs/go-migration.md](go-migration.md)） | Go 1.26 |
| `frontend/` | 画面（Next.js、TypeScript） | Node 24 |
| `db/` | 初期スキーマ（`schema.sql`）とマスタ（`seed_codes.sql`）。空のボリュームで初めて起動したときだけ流れる | Postgres 17 |
| `jobs/image-gen/` | 画像生成の Cloud Run Job（試作。[docs/image-gen-resources.md](image-gen-resources.md)） | Python 3.11 |
| `.devcontainer/` | 開発コンテナと、Docker のソケットプロキシ | |

版の基準は `backend/go.mod`・`.python-version`（ワークフローの検査と `scripts/` のテスト用）・`.node-version`。`scripts/check-versions.sh` が、Dockerfile（`backend/Dockerfile`・`e2e/web.Dockerfile`）・Compose・`engines`・CI と揃っているかを確かめる。
ポートは画面 3200、API 8200（ほかのローカルのサービスと同時に動かすため、固定）。
Compose の `frontend` は、起動のときに依存を `npm ci` で入れる（`package-lock.json` のとおりに入れ、ロックは書き換えない）。`package.json` と `package-lock.json` が前に入れたときと同じなら飛ばす（再起動のたびに入れ直さない）。依存を変えたら、ホストで `package-lock.json` まで更新してから `docker compose restart frontend`（食い違っていると、`npm ci` が止まってログに出る）。

## テスト

```bash
cd backend && TEST_DATABASE_URL=postgresql://postgres@127.0.0.1:55432/postgres go test -tags nodynamic -race -count=1 ./...

cd frontend && npm ci && npm test && npm run build && npm run typecheck   # typecheck は build の後。ホストで実行する（開発コンテナの作業ツリーでは npm ci しない）
```

上の検査と、版の整合・compose の構文・`scripts/` のテストを、まとめて流すには `scripts/verify.sh [段]`（段: `backend` `frontend` `lint` `scripts` `all`）。
CI に近い検査で、同じではない（依存の導入とイメージのビルドをしない。Node・Go やツールの版が違いうる。主な違いは、スクリプト冒頭のコメントに書いてある）。
失敗しても残りの段を流し、最後に結果をまとめる。流せない段（DB・Go・PG 17 クライアント・`node_modules`・`actionlint`・`zizmor` が無い、Next.js の勧告の表示で build が止められた）は「未実行」、CI より弱い点は「注記」に出し、合格とは数えない。
終了コードは 0 = 全部合格・注記なし、1 = 失敗あり（未実行があっても 1）、2 = 失敗は無いが未実行か注記あり、64 = 引数の誤り。
副作用は、テスト用 DB の作り直し（`TEST_DATABASE_URL` は、必ずテスト用のサーバーに向ける）と、`frontend/.next` など Git の管理外のファイル。追跡されている `frontend/next-env.d.ts` は build が書き換えるので、終了時に戻す。
`verify.sh`・`scripts/`・`frontend/package.json` の `scripts`・テストを変えたら、流す前に `git diff` で中身を見る（`.claude/settings.json` の許可で、`scripts/verify.sh` は確認なしで動く）。`scripts/` のテストは CI に入っていない。

Go のテストは、テンプレートの DB を 1 つ作り（`db/schema.sql` と `seed_codes.sql` → migrate → seed --extend → seed-images。`jobnavi_go_template`）、テストごとにそれを複製した DB（`jobnavi_go_t_*`）を使う（書き込みのあるテストが、互いに影響しない。終わりに消す）。
パッケージは 1 つで、`go test` が並列に流すのはテスト関数どうしではなく（`t.Parallel` を使わない）、複数のパッケージ（今は 1 つ）だけ。
- `golden_test.go`: 旧 Python（FastAPI）の API が、同じ手順で作った DB に返した応答（`testdata/golden_read.json`。217 件）を、一字一句そろえて再生する。読み取り系の経路と、404・405・422・CORS の形が対象。
- `unit_test.go`: 期待値は、Python（`json`・Pillow・argon2-cffi・pydantic）が実際に返した値（`testdata/unit_fixtures.json`）や、Pillow と同じ向きの対応。
- `auth_test.go`・`garden_test.go`: JWT の検証（偽の JWKS）、プロフィール、閲覧記録と関連・おすすめ、園のログイン・試行制限・画像 API 一式・`/media`。
- `storage_test.go`: 偽の S3（SigV4 の署名を、受け取ったヘッダーから作り直して照合する）。`s3sign_test.go`: botocore と同じ署名になること。
- `cli_test.go`・`search_test.go`: migrate・seed・運営用の CLI・reconcile・バックアップと復旧（`pg_dump` 17 が要る）、偽の Meilisearch による検索・退避・索引の作り直し。

`TEST_DATABASE_URL`（Postgres 17）が無いと、DB を使うテスト（ほぼ全部）は飛ばされる。飛ばされたまま通っても、検証したことにならない（CI は、スキップが 1 件でもあれば落とす）。
`go.sum` は `go mod tidy` で作り直す（直接編集しない）。依存を足すときは先に確認する。

## ダミーデータ

`seed` と `seed-images` は、開発・テスト専用。`DEMO_DATA=allow` が無いと実行を拒否する。本番の DB に向けない。

```bash
docker compose run --rm seed seed --reset            # ダミーデータだけ入れ直す
docker compose run --rm seed seed --extend           # 求人を 30 件構成（園 12・表示 26・非表示 4）に増やす
docker compose run --rm seed seed --reset --extend   # 入れ直して 30 件にする
```

`--extend` は、足りない分だけ足し、何度実行しても増えない。既存の求人・アカウントは変えない。引数なしの `seed` は 12 件。先に開発 DB のバックアップを取る（[docs/image-storage.md](image-storage.md)）。
園の会員登録の画面・API は無く、園のアカウントは `seed` で作る。

## マイグレーション

適用の前に、必ず DB のバックアップを取る（[docs/image-storage.md](image-storage.md)）。

```bash
docker compose run --rm migrate migrate --status    # 読むだけ
docker compose run --rm migrate migrate --dry-run   # 事前検査だけ
docker compose run --rm migrate                                   # 適用
```

## 検索エンジン（任意）

キーワード（`q`）の検索は、既定では Postgres（`LIKE`。0009 の正規化）です。Meilisearch に切り替えることもできます（2026-11-22 に足した。既定は使わない）。**Postgres が正で、Meilisearch は写し（索引）**です。

```bash
docker compose --profile search up -d meilisearch      # 検索エンジンを起動（backend-net の中だけ。ホストには公開しない）
docker compose run --rm search-index                    # DB から索引を作り直す（何度流しても同じ。入れ替えの間も検索できる）
SEARCH_ENGINE=meilisearch docker compose up -d api      # api を、エンジンを使う設定で起動（常用するなら .env に書く）。戻すときは SEARCH_ENGINE を外して up -d api
```

- **仕組み:** 語を DB の `search_fold`（0009）で畳み、Meilisearch に `locales: ["jpn"]`・全語一致で聞いて、候補の id（関連度の順）を受け取る。表示中か・条件・権限は、これまでどおり SQL が判定し、並びは候補の順。`/api/facets` も同じ候補を使う（1 リクエストにつき 1 回だけ聞く）。
- **退避:** エンジンに届かない・索引が無い・エラー・結果が上限（1,000 件）に届いた・応答が不正（途中で切れる・HTTP でないを含む）のときは、Postgres の `LIKE` で探す（警告ログ。例外の本文は固定の文）。届かない・エラー・不正のときは、10 秒間は聞かずに探す（毎回タイムアウトを待たない）。エンジンを同時に待つのは 4 本まで（超えた分は待たずに Postgres で探す）。Meilisearch の検索構文（語頭の `-`、`"`）を含む語は、エンジンに渡さず Postgres で探す（`LIKE` ではただの文字なので、経路で意味が変わらないように）。エンジンの返事を待つ間は、DB の接続を持たない。リダイレクトは辿らない（API キーを別のホストへ送らない）。`SEARCH_ENGINE` の値や `MEILI_URL` の設定ミスは、別の値に切り替えず、起動で止める。
- **索引:** 表示中の求人だけ、`search_fold` を通した 7 列（職種名・法人名・紹介・所在地・業務内容 1〜3。DB の `recruit_search_text` の対象の列と同じで、片方を変えたらもう片方も直す。テストが見張る）。`api search-index`（`--status` で状態。Compose では `docker compose run --rm search-index`）。いつも別名の索引（`recruits_next`）に全件を入れてから、入れ替え（すでに索引があるとき）か改名（初回）で使う名前にする。途中で失敗しても、一部だけの索引は使われない。求人を足す・変える・非表示にしたあと（`seed` のあとを含む）に流す。流し忘れても、非表示の求人は出ない（新しい求人が出ないだけ）。
- **言語は日本語に固定:** 自動判定だと、漢字だけの語（「温室」）が中国語と判定され、0 件になった（2026-11-22、開発 DB の 26 件で確認）。
- **Postgres との違い（同じ 26 件で比べた）:** Meilisearch は単語単位。1 文字の「室」は 0 件（Postgres は 5 件）。タイポ許容があり、「ガイト」が「ガイド」に当たる（Postgres は 0 件）。「おんしつ」→「温室」（読み）は、どちらも当たらない。それ以外の語（温室・ガイド・半角カナ・ひらがな・`(架空の住所)` など）は、件数が同じ。
- **設定（環境変数）:** `SEARCH_ENGINE`（`postgres` / `meilisearch`。既定 `postgres`。別の値は起動で止まる）、`MEILI_URL`（`SEARCH_ENGINE=meilisearch` のとき必須。http(s) の URL）、`MEILI_API_KEY`（ASCII の印字できる文字だけ。違えば起動で止まる）、`MEILI_TIMEOUT_SECONDS`（既定 2、1〜30）。Compose では `MEILI_MASTER_KEY`（既定は開発用の値）を、`meilisearch` の鍵と、api・`search-index` の `MEILI_API_KEY` に渡す。`meilisearch` は profile: search なので、`.env` で常用するときは `COMPOSE_PROFILES=search` も要る。
- **本番:** 置き場は未決。永続ディスクと常駐が要り、Cloud Run のスケールゼロでは索引が消える。マスターキーは開発用の既定値（秘密ではない）とは別の値にし、API には検索だけのキーを渡す（`MEILI_API_KEY`）。
- **テスト:** 偽のエンジン（`backend/search_test.go` の `fakeMeili`）で、呼び方・並び・退避・索引の作り直しを確かめる。本物のエンジンの検索の質は、CI では確かめない。

## 園のログインの試行制限と CORS

- **試行制限**（`backend/garden.go`。表は `0007_garden_login_attempts.sql`）: メールアドレスごとに数える。IP では数えない（API を呼ぶのは画面のサーバーで、どの利用者も送信元が同じになるため）。窓（既定 15 分。そのメールの最初の試行から）の中で上限（既定 5 回）を超えた試行は、正しいパスワードでも 429（固定の文）。メールが登録済みかどうかで、応答も処理も分けない（登録が無いときも、パスワードの検証と同じ処理を通す）。成功すると、そのメールの数えを消す。窓の 2 倍より古い行は、ログインのついでに消す。
- 表には、正規化（前後の空白を除き小文字）したメールの SHA-256 だけを置き、生のメールは置かない（形は CHECK で縛る）。ハッシュは秘密ではない（推測しやすい文字列は、辞書で元に戻せることがある）ので、表を読める人は絞る。
- 設定（環境変数）: `GARDEN_LOGIN_MAX_FAILURES`（既定 5。1〜1000）、`GARDEN_LOGIN_WINDOW_MINUTES`（既定 15。1〜10080）。整数でない値・範囲外は、API の起動で止まる（別の値には切り替えない）。
- 限界: そのメールを知っている人が、わざと失敗を重ねると、その園のログインを窓の間だけ止められる（正当な利用者も入れなくなる）。IP の制限・CAPTCHA は無い。
- **CORS**（`backend/server.go`）: `CORS_ORIGINS`（既定 `http://localhost:3000`）の許可元に、GET・POST・PUT・PATCH・DELETE を許す。画面は API をサーバー側から呼ぶので、ふだんはブラウザーの CORS を通らない。
- 確認: 試行制限は `garden_test.go`、CORS は `golden_test.go`、設定値は `unit_test.go` の `TestConfigErrors`、表の形・復旧は `cli_test.go`。本番の DB への適用と、実際のブラウザーでの確認は未実施。

## E2E（表示名の保存）

「Google ログイン後の画面で、アカウントのメニューから表示名を変更して保存し、画面と API の DB に反映される」までを、Playwright で自動テストする（`e2e/`）。

- **確かめること**: ログイン済みの画面でアカウントメニューを開き、「表示名を変更」で名前を変えて保存すると、メニューのボタンが新しい名前になり（再読み込みしても同じ）、`users.display_name` が新しい名前になる。対照として、別の秘密で署名した Cookie ではログインしていない表示になること（署名が検査されていること）も確かめる。
- **確かめないこと**: 本物の Google との往復（同意画面・コールバック）。https の Cookie（名前が `__Secure-` で始まる経路）。この E2E は http の `http://web:3000` だけで動く。Google の代わりに、Better Auth のセッションを模した状態（`auth_user`・`auth_session` の行と、署名つきの Cookie）を、使い捨ての DB に注入する（`e2e/support/session.ts`）。
- **実行**: `e2e/run.sh`（リポジトリのルートからでも、どこからでも）。ホストに要るのは Docker だけ（Playwright もブラウザーも Node も要らない）。ホストにポートを公開せず、名前付きボリュームも作らない。開発スタック（`jobnavi`。3200・8200）とは別のプロジェクト（`jobnavi-e2e`）で、開発の DB・コンテナには触らない。
- **しくみ**: `e2e/compose.yml` が、使い捨ての DB（tmpfs。DB 名は `jobnavi_e2e`）・API・画面（`next build` → `next start`）・Playwright を起動し、終わったら `down -v` で全部消す（失敗しても）。ビルドしたイメージ（`jobnavi-e2e-*`）だけ、次回を速くするため残る。初回は、イメージの取得と `next build` で時間がかかる。失敗時の trace・スクリーンショットは、`e2e/test-results`（Git 管理外）に出る。
- **秘密**: Better Auth の署名の秘密と DB のパスワードは、実行ごとに乱数で作る使い捨ての値（ファイルにもログにも出さない）。本物の値（`BETTER_AUTH_SECRET`・Google・R2）は使わない。Google の OAuth の値は、設定の検査を通すための固定のダミー。
- **版を上げたとき**: Cookie の署名の作り方は、Better Auth 1.7.18 の挙動を `e2e/support/session.ts` に写したもの（Better Auth をテスト側には入れていない）。`frontend/package.json` の `better-auth` の版を上げたら、E2E を流して確かめ直す（合わないと「注入したセッションを Better Auth が認めませんでした」で落ちる）。Playwright は、`e2e/package.json` の `@playwright/test` と `e2e/Dockerfile` のイメージの版（どちらも 1.65.1）を必ず同じにする。
- **CI には入れていない**（`ci.yml` は変えていない）。

## 画像の生成

`frontend/scripts/generate-images/`（Gemini の Batch API。課金される。手順は同じ場所の README）。生成した画像は `api images register`（Compose では `docker compose run --rm api images register …`）で登録する。

## CI

[.github/workflows/ci.yml](../.github/workflows/ci.yml)。`main` への push と PR で、バックエンドのテスト（Postgres 17）、バックエンドのイメージのビルド、フロントエンドのテスト・ビルド・型検査、ワークフローの静的検査を行う。外部の API は呼ばず、デプロイは含まない。

## 秘密の検査

コミットの前後に、登録した語（実プロジェクト ID など）が入っていないかを `scripts/check-sensitive-terms.sh` で検査できる。語は Git の管理外の `.git/sensitive-terms` に置く（リポジトリには置かない。エージェントは、この中身を読まず、表示しない）。台帳に書いてよいプロジェクト ID と、登録語に入れた語は別で、登録語に当たるものは書かない。
