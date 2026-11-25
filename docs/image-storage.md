# 画像の管理（求人・法人）

求人と法人の画像を、**本体はオブジェクトストレージ、対応は DB、コードと一覧は Git** に分けて管理する。画像が増えても、コードの修正・再デプロイ・Git への画像の追加は要らない。

```text
ブラウザー ── 画面（Next.js）── API（Go）── DB（images / recruit_images / garden_images）
                 │                    └── 保存先（1 か所。S3 では 1 バケット）
                 └── next/image ── 公開 URL（CDN の独自ドメイン。ローカル開発は /media）
```

## 仕組み

- **園がアップロードした画像は、検証に通れば、保存した時点でそのまま公開される。** 承認の段階は無い。
- **画像の状態**（`images.status`）: `uploading`（登録の途中）→ `approved`（公開中）。ほかに `rejected`（運営が取り下げた）、`deleted`（消した。行は記録として残す）。
  `approved` という名前は DB の制約に合わせて残してあり、意味は「公開中」。旧い値の `pending` / `publishing` は、今は作られない。
- **公開 API が返すのは `approved` だけ。**
- **運営ができるのは、公開後の取り下げだけ**（`api images reject`）。DB を `rejected` にして実体を消す。園の API からは呼べない。
- **登録は 1 つの関数を通る**（`backend/image_service.go`）: 園のアップロード、運営の CLI、ダミー画像の seed が同じ検証・登録を使う。
  検証は、形式（JPEG・PNG・WebP のみ。SVG・GIF・アニメーションは不可）、容量（既定 5MB）、寸法（短辺 200px 以上、4000 万画素まで）、実データ（Go の標準ライブラリと WebP のライブラリで読み込めること）。
  配信用には、EXIF・GPS・ICC・テキストを除いて再エンコードしたものを保存する（向きは画素に反映する。長辺は 2400px まで）。元の画像は残さない。
  **アップロードは API を通す**（ブラウザーから保存先へ直接は送らない）。署名付き URL は使わない。
- **保存キーは `images/<uuid>.<拡張子>`。** 画像の更新は、同じキーに上書きせず、新しい画像として登録して DB の参照を切り替える（保存部品は、同じキーへの別の内容の書き込みを拒否する）。
  公開 URL は保存キーから組み立てる（DB には持たない）。
- **削除はその場で行う。** 園が求人から外した画像は、どこからも使われなければ、その場で `deleted` にして実体を消す（猶予は無い）。元に戻せない。
  公開 URL は `Cache-Control: immutable`（1 年）で配るので、削除後も、ブラウザーなどのキャッシュには、しばらく残りうる。
- **求人の画像**は `recruit_images`（表示順 `position`、ヒーロー `is_hero`）。ヒーローは求人ごとに 1 枚まで（DB の一意索引）で、公開中の画像だけ（DB のトリガー）。
  指定が無ければ、公開 API は表示順で先頭の公開中の画像を `hero_image` にする。`hero_image` は、求人の詳細と、一覧（`GET /api/recruits`）の各項目の両方で返す（一覧のサムネに使う。一覧は、その 1 ページの分を 1 回の SQL でまとめて引く）。画像が 1 枚も無ければ、画面は共通の代替画像（`public/images/generated/job-detail-sample.jpg`。Git 管理）を出す。
- **法人の画像**は `garden_images`（`logo` / `banner`）。同じ `images` を使う。API（`/api/garden/profile-images`）と DB はあるが、**画面と公開ページでの表示は未実装**（法人の紹介ページができてから）。
- **権限**: 園が操作できるのは、自分に紐づく求人（`recruits.garden_account_id`）と自分の画像だけ。確認は API が毎回 SQL の条件で行い、他の園のものは 404（存在も分からない）。
- **画面（`/garden/recruits/<id>/images`）**: 上にドラッグ＆ドロップの領域。ファイルを選ぶ（またはドロップする）と、手元のファイルから保存前のプレビューを出し、代替テキストを入れて「公開する」を押すと API へ送る。
  プレビューのための API は無い（ブラウザーが手元のファイルを表示するだけ）。

## 設定（環境変数）

| 名前 | 意味 |
|---|---|
| `STORAGE_BACKEND` | `local`（既定。フォルダー。開発・テスト用）/ `s3`（S3 互換。Cloudflare R2 など） |
| `STORAGE_LOCAL_DIR` | local の保存先（既定 `/data/media`。Compose では名前付きボリューム `dev-media`。実際の置き場は、その下の `public/`） |
| `IMAGE_PUBLIC_BASE_URL` | 公開画像の URL の頭。s3 では CDN の独自ドメイン（例 `https://img.example.com`）。local は `/media`。**画面（Next.js）にも同じ値を渡す** |
| `STORAGE_S3_ENDPOINT_URL` | S3 互換のエンドポイント。R2 は `https://<アカウント ID>.r2.cloudflarestorage.com`（地域指定のバケットは別の形。後述の出典） |
| `STORAGE_S3_BUCKET` | バケット名（1 つ。置いた画像が、公開 URL から読めるようにしておく） |
| `STORAGE_S3_ACCESS_KEY_ID` / `STORAGE_S3_SECRET_ACCESS_KEY` | 保存先の認証情報。環境変数かシークレット管理でだけ渡す。ログ・Git・画面に出さない |
| `IMAGE_MAX_UPLOAD_BYTES` | アップロードの上限（既定 5MB） |

s3 で必要な設定が 1 つでも欠けると、API は起動に失敗する（local に切り替えない）。開発用と本番用は、**バケットも認証情報も別**にする（本番の名前と分離の方針は未決）。

## 使い方

**園（画面）**: <http://localhost:3200/garden> にログイン →「求人の画像」から求人を選び、画像をドロップ（または「ファイルを選ぶ」）→ プレビューを確認 →「公開する」。
並べ替え・ヒーローにする・削除もここでできる。削除は、その場で消え、元に戻せない。

**運営（CLI。DB と保存先の認証情報を持つ人だけ）**: コンテナの中で実行する（例: `docker compose run --rm api images list`）。

```bash
api images list [--status rejected]
api images register FILE --alt "説明" --recruit-id 5 [--hero]   # 登録した時点で公開される
api images reject <画像ID>       # 公開後の取り下げ（実体も消す）。承認の操作ではない
api images reconcile [--stuck-hours 1]   # 食い違いの報告だけ。何も変えない
api images formfile              # recruits.formfile に値のある求人の一覧
```

**画像を足しても、コードの変更・再デプロイは要らない**（園の画面か CLI で登録すれば、DB の関連だけで表示が変わる）。

## 失敗したときの整合（DB とストレージが食い違ったままにしない）

先に DB へ意図（`uploading`）を書き、そのあとストレージを触る。削除は、先に DB を確定し、そのあとで実体を消す。どの段階で止まっても、次のようになる。

- 登録の途中で失敗 → 作りかけの行と実体を消す。消せなければ `uploading` のまま残り、`reconcile` が報告する（ダミー画像の `seed-images` は、再実行で、実体が揃っていれば公開中にし、無ければ片付けてやり直す）。
- 取り下げ・削除で、実体の削除だけ失敗 → DB は `rejected` / `deleted` で確定していて、公開 API には出ない。残った実体は `reconcile` が報告する。
- 参照されている画像（`recruit_images` / `garden_images`）は消さない（DB も `ON DELETE RESTRICT`）。
- **`reconcile` は報告だけで、何も変えない**（削除の実行や定期実行は無い）。報告するのは、`stuck_uploading`（登録が途中で止まった）、`orphan_objects`（DB に行の無い実体）、
  `stray_objects`（取り下げ・削除済みなのに残っている実体）、`unreferenced`（どこからも使われていない公開中の画像）。新しいもの（10 分以内。`unreferenced` と `stuck` は 1 時間以内）は、登録の最中かもしれないので数えない。

## マイグレーション（`backend/migrate.go`）

番号付き SQL（`backend/migrations/NNNN_名前.sql`。バイナリに埋め込む）と小さな実行器（`api migrate`）。API は生の SQL で ORM を使っていないため、Alembic ではなくこの方式にした（Alembic の利点であるモデルとの差分生成が使えない）。

- `0001` は初期スキーマ（`db/schema.sql` と `db/seed_codes.sql`。Postgres の初回起動で流れる）。実行器はこれを流さず、**表・列・制約・索引・マスタの件数が `baseline_fingerprint.json` と一致したときだけ**「適用済み」と記録する。1 つでも違えば、何も記録せず止まる。
- 適用済みのファイルは sha256 を記録し、書き換えを検出して止まる。直すときは新しい番号のファイルを足す。
- 1 ファイル 1 トランザクション（`lock_timeout` 5 秒）。失敗したら何も変えない。同時実行はアドバイザリーロックで防ぐ。
- 確認: `api migrate --status`（読むだけ）/ `--dry-run`（適用予定と事前検査だけ）/ 引数なしで適用。
- `0002_images.sql` は新しい表の作成と、`recruits.demo_key`（NULL 可・既定値なしの列）の追加だけ。**既存のデータ・ID・`formfile` は変えない。** `recruits` への ALTER は一瞬の排他ロック。DROP・TRUNCATE・リセットは含まない。
- `recruits.formfile`（「画像ファイル」の列）は残してある。値のある求人は `formfile` で確認できる（ダミーデータでは 0 件の想定）。値と使用箇所の確認が済んでから、別のマイグレーションで外す。

## バックアップと復旧

マイグレーションの前に、必ずバックアップを取る（`backups/` と `*.dump` は Git に入れない）。
バックアップは、最新の 1 つだけを、同じ名前へ上書きして持つ（日付つきの名前で増やさない。2026-11-20 にユーザーが決めた個人ルール）。上書きの前に、新しい写しが取れたこと（サイズが 0 でない）を確かめる。

```bash
mkdir -p backups
docker compose exec -T db pg_dump -U jobnavi -Fc jobnavi > backups/jobnavi.dump.new
ls -l backups/jobnavi.dump.new                    # サイズが 0 でないことを確かめる。0 なら、次の行は実行しない
mv backups/jobnavi.dump.new backups/jobnavi.dump   # 古い写しは、これで置き換わる
```

復旧（別の DB へ戻して中身を確かめてから、必要なら入れ替える）:

```bash
docker compose exec -T db psql -U jobnavi -d postgres -c "CREATE DATABASE jobnavi_restore"
docker compose exec -T db pg_restore -U jobnavi --no-owner -d jobnavi_restore < backups/jobnavi.dump
```

- 画像の本体は DB のバックアップに入らない。**ストレージ側の保護**が別に要る（R2 ではバケットの保護の運用を決める）。バケットロックは、取り下げ・削除を妨げるので、開発では掛けない。
- 画像が失われても、DB の行（`storage_key` と `sha256`）から、何が足りないかが分かる。ダミー画像は、下の手順で再生成なしに復元できる。

## 新しい環境（ダミー画像の復元）

通常の `docker compose up` は、マイグレーションもダミーデータも流さない。新しい環境では、README の「初めての環境」の順に、`migrate` → `seed` → `seed-images` を 1 回ずつ実行する（いずれも `profile: tools` の道具で、`up` では起動しない。`seed` と `seed-images` は `DEMO_DATA=allow` が無いと実行を拒否する）。ダミー画像の元ファイルは `frontend/public/images/generated/recruits/`（Git 管理。マニフェストは `backend/demo_images.json`。元ファイルの sha256 が一致しなければ使わない）。
マニフェストは 24 件（今ある求人に結ぶ 13 件と、まだ無い求人の分 11 件）。求人が DB に無い画像は、登録せずに知らせる（どの求人にも結ばれない画像を作らないため）。求人ができてから `seed-images` を再実行すると、その分が登録される。求人 1 と 7 は 2 枚、植物医（予定）は 3 枚で、ヒーローは位置 0 の 1 枚。
同じことは単独でもできる（何度実行しても、画像も関連も増えない）。

```bash
docker compose run --rm seed-images
# 画像の置き場（https。マニフェストの file 名を末尾に足して取る）から復元するとき
docker compose run --rm seed-images seed-images --source-url https://<配信ドメイン>/demo/
```

## Cloudflare R2 を使うとき

開発では、開発用のバケット `hoshiniwa-images` を使っている（`r2.dev`＝Public Development URL を有効にしてある）。API トークン `hoshiniwa-images-rw` の値を `STORAGE_S3_*` に設定し、`STORAGE_BACKEND=s3`、`IMAGE_PUBLIC_BASE_URL` は `r2.dev` の URL にする。
2026-11-01 に、R2 のオブジェクト 11 件が、公開 URL から 200・`image/jpeg`・`Cache-Control: immutable` で返ること、求人詳細（`/recruits/1`）の画像が `/_next/image` 経由で `r2.dev` から取得され、コンソールにエラーが無いことを確認した。
DNS・独自ドメインは未設定。

- 必要なもの: Cloudflare のアカウント（R2 を有効にした状態）。独自ドメインを使うなら、**同じアカウントに zone として登録されたドメイン**（公開バケットの独自ドメインの条件）。
  独自ドメインが無い間の開発では、バケットの Public Development URL（`r2.dev`。レート制限があり、開発用。本番に使わない）を使える。
- **`r2.dev` は開発確認用で、本番では使わない。** URL はバケットごとの固定で、推測しにくいが秘密ではない。レート制限があり、キャッシュ制御（`Cache-Control` の統制・パージ）とアクセス制御（WAF・レート制限・ホットリンク対策）の設定ができない。
  本番は、独自ドメイン（同じアカウントの zone）＋CDN のキャッシュ設計（キャッシュ期間・取り下げ時のパージ）＋アクセス制御に切り替えてから公開する。切り替え時は `IMAGE_PUBLIC_BASE_URL` と Next.js の画像ドメインの許可（`remotePatterns`）を更新する。
  `r2.dev` の無効化は、`PUT /accounts/{account_id}/r2/buckets/hoshiniwa-images/domains/managed` に `{"enabled": false}`。
- 作るもの（星庭ジョブ専用。ほかのプロジェクトのバケット・設定・認証情報は流用しない）: バケット 1 つ。公開設定にして、画像を公開 URL から読めるようにする。
- 分離の記録: docunavi のヒーロー画像は、2026-11-02 の決定から、このバケット `hoshiniwa-images` の prefix `docunavi-hero/` に保存していた（[docs/image-gen-resources.md](image-gen-resources.md) の「保存先の決定」。実際の開始日は未確認）。2026-11-20 に、docunavi 専用のバケット `docunavi-hero` に分けた（報告。台帳 3.4）。
  ホストは、`hoshiniwa-images` と、トークン `hoshiniwa-images-rw` を、星庭ジョブ専用に戻ったものとして扱う。旧バケットの `docunavi-hero/` にあった 8 件は削除済みで、`images/` は残っている。旧バケット内の存在しないオブジェクト（`docunavi-hero/_nonexistent_probe`）への HEAD は、docunavi の親トークン `docunavi-hero-parent` の鍵でも、旧バケット向けの write の一時キーでも 403 だった（報告。バケット自体への HEAD ではない。Put・Get・Delete・一覧は未試験。台帳 3.4）。
  方針との関係は [access-separation.md](access-separation.md) の「分離済み（docunavi との R2。2026-11-20）」に記録した。トークン `hoshiniwa-images-rw` は、失効しないと、ユーザーが 2026-11-20 に決めた（台帳 3.4）。未確認: 版 5 が `hoshiniwa-images-rw` の鍵なら、その写しが docunavi 側の Secret に残り、R2 で有効なままの可能性がある。旧 `docunavi-hero/` の画像が、削除前に `r2.dev` の公開 URL で読めたかも未確認（台帳 3.4）。
- API トークン: R2 の **Object Read & Write** を、**そのバケットだけ**に絞って作る（Admin 権限は使わない）。作成直後にだけ表示される Secret Access Key は、会話・Git・ログ・スクリーンショットに出さず、環境変数かシークレット管理へ直接入れる。
- 費用の見積もり条件（出典: Cloudflare 公式の料金ページ。等級 B）: Standard ストレージ 10 GB-月・Class A（書き込み・一覧）100 万回/月・Class B（読み取り）1000 万回/月が無料枠、超過分は $0.015/GB-月・Class A $4.50/百万回・Class B $0.36/百万回、**外向きの転送（egress）は無料**。現在の画像は約 10MB で、無料枠に収まる見込み。支払い方法の登録が要るかは、公式ページに記載がなく未確認。
- 出典: <https://developers.cloudflare.com/r2/pricing/> / <https://developers.cloudflare.com/r2/buckets/public-buckets/> / <https://developers.cloudflare.com/r2/api/tokens/>

## 未実装・未確認

- R2 実機での読み書き・公開 URL での配信（`r2.dev`）は確認済み（2026-11-01）。**未確認**: CDN のキャッシュ、独自ドメイン、WAF・レート制限（本番化のときに設定する）。S3 互換の部分の自動テストは、偽の S3（`storage_test.go`。署名は botocore と一致することを `s3sign_test.go` で確かめた）のまま。
- 法人画像の画面と公開ページでの表示。
- 食い違いの報告（`reconcile`）の定期実行（今は手で実行する報告だけ）。ストレージの保護・復旧の運用（R2 側）。
- 画像のウイルス・不適切な内容の検査（形式・寸法・実データの検証だけ）。承認が無いので、公開後の取り下げ（運営の CLI）だけが安全弁になる。
