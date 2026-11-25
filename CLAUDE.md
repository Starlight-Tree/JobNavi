# CLAUDE.md

JobNavi（旧称: 星庭ジョブ。リポジトリ・ディレクトリ・Docker・DB の名前は、すべて jobnavi。旧名 hoshiniwajob への改名は 2026-11-21）。架空の宇宙の植物園・ガーデンの求人サイト（運営は架空の「星庭ワークス」）。データはすべてダミー。
Go（`backend/`。標準ライブラリ + pgx。2026-11 に Python から置き換えた: [docs/go-migration.md](docs/go-migration.md)）、Next.js + TypeScript（`frontend/`）、Postgres 17（`db/`）、Docker Compose。入口は [README.md](README.md)（日本語は [README.ja.md](README.ja.md)）、開発・テスト・ダミーデータ・CI は [docs/development.md](docs/development.md)、
画像・マイグレーション・バックアップ・R2 の詳細は [docs/image-storage.md](docs/image-storage.md)、画面の調査は [docs/ux-research.md](docs/ux-research.md)、
資格情報・権限・MCP をプロジェクトごとに分ける方針は [docs/access-separation.md](docs/access-separation.md)。
**Vertex AI の画像生成ジョブ（デプロイ済みの設定・成功した実行・モデル）の正本は [docs/image-gen-resources.md](docs/image-gen-resources.md)。** 会話の履歴やログを探す前に、これを読む。
領域ごとの規約は `backend/` `frontend/` `jobs/image-gen/` `.devcontainer/` の各 `CLAUDE.md`、パス別の規則は `.claude/rules/`、読み取り専用の調査・レビュー担当は `.claude/agents/`、文書ごとの正本の表は [docs/CLAUDE.md](docs/CLAUDE.md)。

## よく使うコマンド

| 目的 | コマンド |
|---|---|
| 起動（ホストで） | `docker compose up -d --build`。画面 <http://localhost:3200>、API の死活確認 <http://localhost:8200/health>（FastAPI の `/docs` は、Go への置き換えで無くなった）。マイグレーションもダミーデータも流さない |
| 初めての環境（空の DB） | `docker compose up -d db` → `run --rm migrate` → `run --rm seed` → `run --rm seed-images` → `up -d --build`（README の「Quick start」） |
| バックエンドのテスト | `cd backend && TEST_DATABASE_URL=<Postgres の URL> go test -tags nodynamic -race -count=1 ./...`（Go 1.26 以上）。**`TEST_DATABASE_URL` が無いと DB のテスト（ほぼ全部）がスキップされる**。道具は `docker compose run --rm migrate\|seed\|seed-images\|search-index`、運営用の画像 CLI は `docker compose run --rm api images …`（[docs/go-migration.md](docs/go-migration.md)） |
| フロントエンドの検査 | `cd frontend && npm ci && npm test && npm run build && npm run typecheck`（`typecheck` は `build` の後。**ホストで。開発コンテナの作業ツリーでは `npm ci` を実行しない**: `.claude/rules/frontend.md`） |
| 検査（まとめて） | `scripts/verify.sh [段]`（段: backend / frontend / lint / scripts / all）。CI に近い検査で、同じではない（違いはスクリプト冒頭）。流せない段は「未実行」、CI より弱い点は「注記」に出る。終了コードの意味は `scripts/verify.sh` の冒頭。流す前に、`verify.sh`・`scripts/`・テストの変更を `git diff` で見る（`.claude/settings.json` の許可で、確認なしで動く） |
| 版の整合 | `scripts/check-versions.sh`（Python・Node の版が、各ファイルで揃っているか） |
| 登録語の検査 | `scripts/check-sensitive-terms.sh [A..B]`（語は Git の管理外の `.git/sensitive-terms`。表示しない） |

## 構成（1 つのリポジトリに複数のサービス）

| 場所 | 役割 | 版 |
|---|---|---|
| `backend/` | API（Go）。認証、求人、園、画像、マイグレーション、seed、運営用 CLI（同じバイナリ `api` のサブコマンド） | Go 1.26.10 |
| `frontend/` | 画面（Next.js）。API は画面のサーバー側から呼ぶ。画像の一括生成の道具は `frontend/scripts/generate-images/` | Node 24 |
| `db/` | 初期スキーマ（`schema.sql`）とマスタ（`seed_codes.sql`）。空のボリュームで初めて起動したときだけ流れる | Postgres 17 |
| `jobs/image-gen/` | 画像生成の Cloud Run Job のコード（試作。`test_main.py` はあるが、CI には入れていない）。**デプロイ済みのジョブとは別物のことがある。正本は [台帳](docs/image-gen-resources.md)** | Python 3.11（**揃えていない。保留中**） |
| `.devcontainer/` | 開発コンテナと、Docker のソケットプロキシ | |
| `backups/` | DB のバックアップ。Git 管理外 | |

版の基準は `backend/go.mod`（Go 1.26.10）、`.python-version`（Python 3.13。ワークフローの検査と `scripts/` のテスト用）、`.node-version`（Node 24）。各ファイルとの整合は `scripts/check-versions.sh` が確かめる。
開発コンテナの Node は feature `node:2` の既定で、24 に固定していない。実測は v26.13.1（2026-11-20、このコンテナの `node --version`）。基準の 24 と違うので、コンテナでの `npm` の結果は、ホスト・CI（24）と同じとは限らない。

## 守ること（コードを書く前に）

- **`.env`・`backups/`・`.git/sensitive-terms`（登録語）は読まない。** `.env` には R2 の認証情報が入りうる。`backups/` は個人情報を含みうる DB のダンプ。登録語は、実プロジェクト ID などを含みうる（検査は `scripts/check-sensitive-terms.sh` が行う）。旧 PHP の資料は 2026-11-20 に削除済みで、残す参考は [docs/legacy-reference.md](docs/legacy-reference.md)。
  `grep -r .` や `find .` で潜らない（`git grep` / `git ls-files` を使う）。値を表示・ログ・コミットしない。
- **認証情報は環境変数で渡す。** コード・文書・コミット・スクリーンショットに書かない。`STORAGE_S3_*` の値は特に。画面（`frontend/`）に渡す秘密は、ログイン（Better Auth）の `BETTER_AUTH_SECRET`・`GOOGLE_CLIENT_SECRET`・`DATABASE_URL` だけ（サーバー側の環境変数。`NEXT_PUBLIC_` を付けない）。`STORAGE_S3_*`・R2 の値など、ほかの秘密は渡さない。
- **`BETTER_AUTH_SECRET` は、決めたら変えない。** DB の署名鍵（`auth_jwks`）と Google のトークンの暗号化に使っている。変えると全員がログアウトに見え、直すには `auth_jwks` の行を消す必要がある（承認が要る）。登録は `scripts/login-secrets.sh`（ユーザーが自分の端末で実行する。エージェントは実行せず、値を見ない）。
- **ダミーデータの投入は `DEMO_DATA=allow` を渡した開発・テストの環境だけ。** `seed` と `seed-images` を本番の DB に向けない。Compose の `tools` の道具は `up` で起動しない。
- **マイグレーションは番号付き SQL を足すだけ。** 適用済みのファイルを書き換えない（sha256 を記録していて、変わると止まる）。`DROP` / `TRUNCATE` は足さない。
  `db/schema.sql` は 0001 の基準で、`baseline_fingerprint.json` と照合される。適用の前に、必ず DB のバックアップを取る（`docs/image-storage.md`）。
- **`backend/go.sum` は直接編集しない。** `go mod tidy` で作り直す。依存を足すときは、先に確認する。ビルドは `-tags nodynamic`（WebP の共有ライブラリを探さない）。
- **エラー本文は固定の文。** 例外の文字列やサーバーのパスを API の応答に含めない。
- **権限は API が毎回確認する。** 園は、自分に紐づく求人と画像だけを扱える。他の園のものは 404（存在も分からない）。利用者と園は、アカウントもセッションも Cookie も別。
- **実 API・課金・外部への書き込みは、事前に承認を得る。** 画像の一括生成（Gemini の Batch API）、R2 への書き込み、本番の DB・デプロイ。件数・回数・概算費用を先に示す。
- **画像生成のデプロイ・モデルの変更・保存先の変更は、[台帳](docs/image-gen-resources.md)を更新するまで「完了」にしない。** 台帳の「6. 変更したときの完了条件」に従う。
  デプロイした・実行した・生成に成功した・保存と回収ができた・画面に表示できた、は別々に記録する。成功版の識別は、イメージのダイジェストで行い、`latest` などのタグだけにしない。
  確認は `scripts/inspect-image-gen.sh`（読み取り専用）。担当 AI は、日常の ID では `imagen-app-2026` を読めないので、利用者が実行する。
- **ポートは 3200（画面）と 8200（API）。** 他のローカルのサービスと同時に動かすため。変えない。
- 変更したら、触れた範囲のテストを実行してから報告する。確認できていないこと（Docker のビルド、本物のブラウザーなど）は、確認済みと書かない。

## 保留中（勝手に直さない。方針を示して承認を得てから）

- `jobs/image-gen/`: Python の版（3.11）、モデル名（`gemini-3.1-flash-lite-image`。`frontend/scripts/generate-images/` は `gemini-nano-banana-2.1`）、
  既定のプロジェクト ID がコードに書かれていること。
- 画像生成の「下書き用」モデル（安いモデル）の選定: 記録が見つかっていない（台帳の 2.1 節）。**モデルの確定は保留中**（2026-11-02 の決定）。

## 承認が要る操作（正本）

承認は、その操作だけに有効（前の承認は次に及ばない）。サブエージェントには任せない（[.claude/rules/parallel-work.md](.claude/rules/parallel-work.md)）。

| 操作 | 承認 | 強制の手段 |
|---|---|---|
| コミット | 要る（`git diff --staged` を見せてから）。`--no-verify` は使わない | 承認は文章。`--no-verify` と秘密の混入は `guard-git-write.py` が拒否 |
| `git add -A` / `git add .` | 使わない（パスを指定する） | `guard-git-write.py` が拒否 |
| プッシュ、`git reset`、`git restore` | 要る | `settings.json` の `ask` |
| 強制プッシュ（`--force` など）・公開済み履歴の書き換え | 原則しない。ユーザーが明示して頼んだときだけ、対象と理由を示して | `ask` と `guard-git-write.py` の確認 |
| `CLAUDE.md`・`.claude/`（規則・フック・権限・スキル）の変更 | 要る（差分を示してから） | `CLAUDE.md` は `settings.json` の `ask`。`.claude/` は Claude Code 本体の保護パス（公式文書による。実機は未確認） |
| 実 API・課金、R2 への書き込み、本番の DB・デプロイ | 要る（件数・回数・概算費用を先に） | 文章 |
| 本番の DB へのマイグレーションの適用 | 要る（操作ごと。DB のバックアップが先） | 文章 |
| 開発・テストの DB への `migrate`、`seed` / `seed-images`（`DEMO_DATA=allow`） | 依頼があれば要らない（DB のバックアップが先。本番に向けない） | 文章。`seed` は `DEMO_DATA` が無いと拒否 |
| gcloud・gh・wrangler・infisical・prisma・MCP の書き込み系 | 既定は実行せず、コマンドを提示する。ユーザーが明示して頼んだときだけ、示して承認を得てから実行 | フック（`guard-cloud-cli.py`・`guard-deploy-cli.py`）と `ask` |
| 使われているか分からないファイル・データの削除、元に戻す操作 | ユーザーの明示的な指示があるまで実行しない | 文章 |
| 新しいサービス・言語・フレームワーク・DB、保留中の項目、画像生成のモデル・保存先の変更 | 要る | 文章 |
| 開発コンテナの作業ツリーでの `npm ci` | しない（ホストの `node_modules` を上書きしうる。ホストで行う） | `ask`（`Bash(*npm ci*)`。`bash -c '…'` などは通る） |

## Git

- **コミットメッセージ:** Conventional Commits（`type(scope): 英語の命令形の要約`）。1 コミット 1 論理変更。本文に「なぜ」と確認の結果（限界も）を書く。
- **運用:** `main` に直接コミット。ステージはパスを指定する（`git add -A` と `git add .` は使わない）。コミット前に `git diff --staged` で、意図しない変更が入っていないか確かめる。
- **プッシュの前に、** 追加行の秘密情報と `scripts/check-sensitive-terms.sh origin/main..HEAD` を実行する（文章の規則。強制していない。プッシュは `ask` が毎回止めるので、そのとき実行する）。プッシュ・本番デプロイ・`git reset` / `git restore` の承認は、上の表。
- **CI:** `.github/workflows/ci.yml`（`main` へのプッシュと PR。内容は [docs/development.md](docs/development.md)）。外部 API は呼ばない。デプロイは含まない。ローカルでは `scripts/verify.sh` が、CI に近い検査を流す（同じではない）。

## 作業の進め方

- タスクで指定された範囲だけを編集する。範囲外に必要な変更があれば、勝手に直さず提案する。
- 未コミットの差分は、頼まれない限り触らず、コミットにも含めない。
- 新しいサービス・言語・フレームワーク・DB を足す前に、ユーザーに確認する。
- 既存の文書は、新規作成より更新を優先する。この CLAUDE.md は短く保ち、詳細は領域の `CLAUDE.md` と `docs/` に置く。
- wrangler・infisical・prisma の書き込み系は、フック `.claude/hooks/guard-deploy-cli.py` が確認に回す（docunavi と同じファイルのコピー。直すときは docunavi 側のテスト `tests/test_guard_deploy_cli.py` を先に直し、両方をそろえる）。止められたら回避せず、ユーザーに伝える。
