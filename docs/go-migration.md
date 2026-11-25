# API の Go への置き換え（2026-11-26）

JobNavi の API（FastAPI + psycopg3）を Go に置き換えた記録。Go は `backend/` に置く（旧 Python の `backend/` は削除し、作業中の `backend-go/` を `backend/` に改名した。旧コードは Git の履歴（`4dc1c0f`）に残る）。DocuNavi の方針（今後作る API は Go。解析だけ Python）にそろえた。ユーザーの指示（2026-11-26「JobNavi も Go にリプレイスして」。範囲は seed・運営用 CLI を含めて全部）による。

## 範囲

| 対象 | 置き換え |
|---|---|
| API（求人の検索・詳細・条件ごとの件数・マスタ、ログイン状態の確認と表示名、閲覧記録・関連・おすすめ、園のログインと画像の管理、`/media`） | **Go** |
| マイグレーション実行器・ダミーデータ（`seed`・`seed-images`）・運営用の画像 CLI・検索エンジンの索引づくり | **Go**（同じバイナリ `api` のサブコマンド） |
| 画面（Next.js）、画像生成の Cloud Run Job（`jobs/image-gen/`）、`scripts/` の Python | そのまま |
| DB（スキーマ・マイグレーション 0001〜0009）、HTTP の経路・応答の形・エラーの文 | **変えない**（画面は一行も変えていない） |

## 構成（`backend/`。1 つの `package main`）

| ファイル | 役割（元の Python） |
|---|---|
| `main.go` | 起動・サブコマンドの振り分け・`healthcheck`（`main.py`） |
| `config.go` | 環境変数。設定ミスは別の値に切り替えず起動で止める（`settings.py`） |
| `server.go` | 経路・404/405/500・422（pydantic 互換）・CORS（Starlette 互換） |
| `recruits.go` `images_read.go` `search_engine.go` | 求人・件数・マスタ・公開の画像・Meilisearch の候補（`recruits.py` `image_queries.py` `search_engine.py`） |
| `jwt.go` `auth.go` `body.go` | JWT の検証（ES256 / RS256・JWKS）・プロフィール・JSON 本文の検査（`jwt_auth.py` `auth.py`） |
| `events.go` | 閲覧記録・関連・おすすめ・埋め込み（`recruit_events.py` `recruit_embeddings.py`） |
| `garden.go` `security.go` `email.go` `uuidparse.go` | 園のログイン・試行制限・argon2id（`garden.py` `security.py`） |
| `images_api.go` `image_service.go` `imageproc.go` `storage.go` `s3sign.go` | 園の画像 API・登録・検証と再エンコード・保存先（local / S3）・SigV4（`images_api.py` `image_service.py` `storage.py`） |
| `migrate.go` `migrations/` | マイグレーション（SQL は `go:embed`。`migrate.py`） |
| `seed.go` `seed_data.go` `demo_images.json` `imagescli.go` `search_index.go` | `seed_demo.py` `seed_images.py` `images_cli.py` `search_index.py`。`seed_data.go` は Python のデータから生成した |

依存は、`pgx`（DB）・`golang.org/x/crypto`（argon2）・`golang.org/x/image`（縮小）・`golang.org/x/text`（NFKC）・`github.com/gen2brain/webp`（WebP の符号化・復号。CGo なしの純 Go。**ビルドは `-tags nodynamic`**）。

## 使い方の違い

| 以前 | いま |
|---|---|
| `python -m app.migrate [--status\|--dry-run]` | `api migrate [--status\|--dry-run]`（Compose: `docker compose run --rm migrate`） |
| `python -m app.seed_demo [--reset] [--extend]` | `api seed [--reset] [--extend]` |
| `python -m app.seed_images --source-dir DIR` | `api seed-images --source-dir DIR` |
| `python -m app.images_cli list\|register\|reject\|reconcile\|formfile` | `api images list\|register\|reject\|reconcile\|formfile` |
| `python -m app.search_index [--status]` | `api search-index [--status]` |
| `uvicorn --reload`（ファイルを変えると反映） | 無い。`docker compose up -d --build api` |
| `http://localhost:8200/docs`（FastAPI の自動文書） | 無い。`/health` だけ。経路の一覧は `docs/development.md` |
| `pytest`（305 件） | `go test -tags nodynamic -race ./...`（`docs/development.md` の「テスト」） |

## 同じであることの確かめ方

実装の途中から終わりまで、旧 Python の API を**基準**にして、同じ DB・同じ要求を両方に流し、応答をバイト単位で比べた（差分テスト）。

- 読み取り系: 217 要求 × 2 設定（Meilisearch / Postgres）で差分 0（件数・並び・422・404・405・CORS を含む）。
- 認証・閲覧記録・おすすめ: 223 要求（JWT の改ざん・期限・aud・kid・alg など）と、DB の状態（`users`・`recruit_events`）で差分 0。
- 園・画像: 174 要求（ログイン・試行制限・登録・並べ替え・ヒーロー・削除・上限・プロフィール画像・`/media`）と、DB の状態で差分 0。
- 道具: 空の DB から同じ手順（migrate → seed → seed --extend → seed-images → images → reconcile → search-index → seed --reset）を流し、出力・DB の状態・`pg_dump -s` が一致。
- S3 の署名（SigV4）: botocore が同じ鍵・時刻・ヘッダーで作る署名と一致（5 ケース）。
- 画面から: `e2e/run.sh`（Go の API・Playwright）が合格。
- 画像の見た目（2026-11-26）: JPEG 3 枚・PNG 2 枚（透過つき 1 枚）・WebP 1 枚（EXIF の向き 6 の JPEG と、長辺 4000px の縮小を含む）を、同じ入力で Pillow と Go に通して比べ、並べた図を目で見た（ブラウザーの画面では見ていない）。JPEG は元との差が 45〜48 dB で見分けがつかず、PNG は差 0、向きは同じ。**WebP は色がずれていた**: ライブラリ（`gen2brain/webp` v0.6.4）の `Decode` が WebP 本来の YUV（16〜235）で返すのを、Go の標準の変換が 0〜255 として読み、黒が 16・白が 235 に縮んでいた（元との差 28.6 dB）。`DecodeAll`（libwebp が RGBA にした結果）に切り替えて直し、Pillow と同じ水準（42.4 dB・平均 RGB が一致）になった。回帰テストは `TestDecodeWebPKeepsRangeAndAlpha`。
- 実 R2（2026-11-26）: 開発バケットの `jobnavi-go-verify/` に 1 件（242 KB）を、Go の `s3Storage` で put → 存在 → get（内容が一致）→ 一覧 → delete → 存在しない、の順に確かめ、全部成功した。置いたものは消して、残りは 0 件。公開 URL（`r2.dev`）からの配信は、Go の API が配らないので、ここでは確かめていない（Python の API で 2026-11-01 に確認済み。[image-storage.md](image-storage.md)）。

差分テストの Python の応答は、`backend/testdata/golden_read.json`（読み取り系 217 件）と `unit_fixtures.json`（JSON の構文エラーの位置、Pillow の縮小の大きさ、埋め込みの浮動小数点、argon2 のハッシュ）に固定し、Go のテストが再生する。Go のテストは 44 件（テスト関数。2026-11-26 に `go test -list` で数えた）。

## 意図して違う点

- **画像の再エンコード**: メタデータ（EXIF・ICC）を除き、向きを画素に反映し、長辺 2400px まで縮める点は同じ。ただし出力のバイト列は Pillow と同じにならない（JPEG は baseline・縮小は CatmullRom・不透明な PNG は RGB で保存。Pillow は progressive・Lanczos）。画像の見た目の比べ方は、「同じであることの確かめ方」の画像の項。
- 存在しないファイルを `images register` に渡したとき、Python はトレースバックで落ち、Go は 1 行のメッセージを出す。
- メールアドレスの 422 の文（園のログイン）は、email-validator の主な場合（`@` の有無・前後が空・空白や記号・ピリオド・ハイフン・長さ・括弧・引用符・特別な用途の名前・数字だけの TLD・`名前 <アドレス>`）だけ同じ。IDNA の変換（`xn--` の復元）はしない。
- JWT の `exp`・`nbf`・`iat` が文字列などの数値以外のときは拒否する（PyJWT は数字の文字列を数値として読む。署名つきなので Better Auth は出さない）。
- 設定ミス（`AUTH_*` が URL でない、など）は、Python ではリクエストの途中で例外になるものも、Go では起動で止める。
- 405 の応答、500 の本文（`Internal Server Error`）、`Vary` などのヘッダーは、FastAPI / Starlette に合わせた（Starlette は 500 に `Vary` を付けない、など）。

## 未確認

- 速さ・メモリ（Python との比較）。測っていない。
- 本番（JobNavi に本番環境があるかは、リポジトリから確認できない。`docs/development.md` を参照）。
- 画像の見た目を、ブラウザーの画面で（求人詳細・園の画面）見ること。再エンコードの出力は、上の比較（並べた図）で確かめた。

## 戻し方

旧 Python の API は、Git の履歴にある（置き換えの直前は `4dc1c0f`）。DB のスキーマは変えていないので、Compose の `build` を元に戻せば、同じ DB で動く。
