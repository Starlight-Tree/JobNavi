# frontend/（画面）

全体の方針・禁止事項は、ルートの [CLAUDE.md](../CLAUDE.md)。画像の仕組みは [docs/image-storage.md](../docs/image-storage.md)。画面の調査の記録は [docs/ux-research.md](../docs/ux-research.md)。閲覧の記録・関連する求人・おすすめの仕様は [docs/recommendation.md](../docs/recommendation.md)。

Next.js 16（App Router）、React 19、TypeScript 7、Vitest 5。Node 24（`engines` は `^22.12.0 || ^24.0.0 || >=26.0.0`。基準は 24）。

## 構成

| 場所 | 役割 |
|---|---|
| `app/(site)/` | 公開側。求人検索（`page.tsx`。しぼり込み `FilterForm` は、DocuNavi の資料一覧と同じ、上の横並びのチップ。押すと複数選べる一覧が開く）と求人詳細（`recruits/[id]`）。左のサイドパネル（`AppSidebar`。DocuNavi と同じく、開いた表示と細い帯をボタンで切り替える。はじめは開いた状態で保存しない。開閉できるのは DocuNavi と同じ 1024px 以上。1023px 以下は上の横帯）と、アカウントのメニュー（`AccountMenu`。テーマの切り替えもここ） |
| `app/(site)/diagnosis/` | 3 つの質問の診断（`/diagnosis`。ログイン不要）。GET のフォーム（`DiagnosisForm`）で、答えを URL のクエリ（`employment` `area` `occupation`）に入れる。順位づけは `src/lib/diagnosis.ts` の `rank()`（合った条件の数を数えるだけ。AI・学習は使わない）。対象は API の先頭 50 件、結果は上位 6 件 |
| `app/(site)/@panel/` | 求人の詳細パネル（並行ルート）。一覧と診断の結果のカード（`SelectableCard`）を押すと、同じ URL（`/recruits/ID`）が一覧の右で開く（`(.)recruits/[id]`。インターセプト）。直接開く・再読み込みは全ページ（`recruits/[id]/page.tsx`）。中身は共通の `RecruitDetailView` |
| `app/(auth)/` | 利用者のログイン（Google だけ。DocuNavi と同じ。はじめてのログインで登録される）。「Google でログイン」は、サーバーアクション（`googleLoginAction`）のフォーム。`/register` は `/login` へ移すだけ。サイドパネルは出さず、上の帯に「← 求人を探す」とテーマの切り替えだけ |
| `app/api/auth/[...all]/` | Better Auth の入口（Google への行き来・Cookie・JWKS `/api/auth/jwks`）。Next.js の公式の組み込み方。設定が足りないときは 503。設定と DB 接続は `src/lib/auth.ts`（利用者の表は `auth_` 付き。`backend/app/migrations/0004_better_auth.sql`）。API へは、`src/lib/session.ts` の `userToken()` が `auth.api.getToken` で取る短命の JWT を、Bearer で渡す |
| `app/garden/` | 園。ログイン、ダッシュボード、求人ごとの画像の管理（`recruits/[id]/images`）。サイドパネルは `(site)` と同じ部品（ログイン前は行き先を出さず、アカウントのメニューはテーマだけ） |
| `src/actions/` | サーバーアクション（`'use server'`）。フォームの受け口で、検証してから API を呼ぶ |
| `src/lib/` | `api.ts`（API の呼び出し。サーバー側だけ）、`session.ts`（Cookie）、`search.ts`（URL の条件）、`validation.ts` ほか |
| `src/components/` | 部品。テストは同じ場所の `*.test.tsx` |
| `public/images/generated/` | 生成済みの画像。Git 管理。`seed-images` の元ファイルでもある |
| `scripts/generate-images/` | 画像の一括生成（Gemini の Batch API。課金される）。手順は同じ場所の README |

## 守ること

- **API はサーバー側（サーバーアクション・Server Component）から呼ぶ。** ブラウザーは API を直接呼ばないので、CORS に頼らない。ログインの印は Better Auth の HttpOnly の Cookie で、ブラウザーの JavaScript に渡さない。API へ渡すのは、そこから取る 15 分の JWT。園のログインは別（`hj_garden_session`。トークンは HttpOnly の Cookie）。
- **`NEXT_PUBLIC_*` に秘密を入れない。** 今は 1 つも使っていない。バンドルに埋め込まれ、誰でも読める。環境変数は `API_BASE_URL` `IMAGE_PUBLIC_BASE_URL` `SESSION_COOKIE_SECURE` と、ログイン（Better Auth。画面のサーバーだけが読む）の `BETTER_AUTH_URL` `BETTER_AUTH_SECRET` `GOOGLE_CLIENT_ID` `GOOGLE_CLIENT_SECRET` `DATABASE_URL`（api・migrate と同じ資格情報。表ごとの権限分けはしていないので、`auth_` の表だけを使うのは、コードの上の約束）。**どれも `NEXT_PUBLIC_` を付けない。画面のコンテナが DB を使うのは、ログインのためだけで、求人などのデータは API 経由で読む。**
  `BETTER_AUTH_SECRET` は、決めたら変えない（理由と、変えたときの直し方はルートの `CLAUDE.md`）。migrate の前に画面を使うと、画面を再起動するまで直らない。握りつぶす例外は `lib/auth-log.ts` でログに 1 行出す。Cookie 名の接頭辞は `jobnavi`（既定の `better-auth` だと、同じ localhost の DocuNavi と取り合う）。
- **園のセッション Cookie（`hj_garden_session`）の `Secure` は、`src/lib/cookie-secure.ts` の `sessionCookieSecure()` が決める。** 本番（`NODE_ENV=production`）は既定で `Secure`。外せるのは `SESSION_COOKIE_SECURE=false` と明示したときだけ（http で本番ビルド〔`next start`〕をローカルで動かすとき）。`true` はどの環境でも `Secure`。それ以外の値（`1`・`yes`・空文字）は明示とみなさず `NODE_ENV` に従う（打ち間違いで本番の `Secure` が外れないように）。開発（`next dev`・Compose の `frontend`）は既定で `Secure` なし。対象は園の Cookie だけで、Better Auth の Cookie（`jobnavi.*`）は別。
- **`IMAGE_PUBLIC_BASE_URL` は API と同じ値にする。** `next.config.ts` が、外部画像の許可（`remotePatterns`）を、その配信元の `images/` だけに限る。広げない。
- **`next.config.ts` の `agentRules: false` を外さない**（`next dev` が `AGENTS.md` を自動で作るのを止めている）。`logging.serverFunctions: false` も、メールなどを端末に出さないための設定。
- **テーマ（ライト / ダーク / システム）は `src/lib/theme.ts` に集約する。** 色は `app/globals.css` の変数だけで決め、本体に色（`#xxxxxx`・`rgba`）を直書きしない（`theme.test.ts` が落とす）。ダークの値は、OS 追従（`@media`）と `:root[data-theme='dark']` の 2 か所に同じ内容で書く（これも `theme.test.ts` が一致を確かめる）。新しい色の変数は、ライトとダークの両方に足し、文字は WCAG AA（4.5:1）を満たす。保存先のキーは `jobnavi-theme`。描画前のスクリプト（`app/layout.tsx`）が同じキーを読む。
- **求人の詳細パネル（`app/(site)/@panel/`）:** 1280px 以上は一覧の右に並ぶ非モーダル、1279px 以下は全画面のシート（`role="dialog"`）。境目は `RecruitPanel.tsx` の `WIDE` と `app/globals.css` の `@media` の 2 か所なので、変えるときは両方。カードを出す画面を `(site)` に足したら、同じ場所に空の `@panel/<パス>/page.tsx` を足す（足さないと、その画面でパネルを閉じても URL が戻らない）。
- **一覧の下のページ送り（`PagerBar`）は `position: sticky` の固定バー**（`app/globals.css` の `.pager`）。`.results` の中に置き、最後のカードの直下で止まる。`?page=N` への通常のリンクで、0 件のときは出さない。重ね順は、しぼり込みの一覧（20）・詳細パネル（`--peek-z`）より下。
- **閲覧の記録は、クライアント部品 `ViewBeacon` が `useEffect` から 1 回だけ、サーバーアクション `recordViewAction`（`src/actions/recruit-events.ts`）を呼ぶ。** 描画のときには呼ばない（プリフェッチ・ボットを数えないため）。全ページとパネルの両方に置く。未ログインは API を呼ばず、失敗は握りつぶす。同じ求人を 30 分以内に何度送っても、API が 1 件にまとめる。
- **「関連する求人」「あなたへのおすすめ」は付随の表示。** 取れなければ節ごと出さず、画面は壊さない（`src/lib/load-related.ts` は例外を投げず、取れなければ `[]`）。順位づけと文の根拠は [docs/recommendation.md](../docs/recommendation.md)（機械学習は使っていない）。
- **画面の検証は API の検証の代わりにならない。** `garden-images.ts` の形式・容量の検査は手前の確認で、本当の検査は API。
- **`public/images/generated/recruits/` の画像を差し替える・生成し直すときは、`backend/app/demo_images.json` の `sha256` も同じ変更で更新する。** 一致しない画像は `seed-images` が使わない。
- 画像の一括生成は課金される。実行の前に、枚数と概算費用を示して承認を得る（`--dry-run` は課金なし）。キー（`GEMINI_API_KEY`）は `scripts/generate-images/.env`（Git 管理外）か環境変数で渡し、表示しない。
- 画面を変えたら、前例（`docs/ux-research.md`）にならって Playwright で確かめる。基準は幅 1280px。390px は横スクロールが出ないことの確認だけ。

## テストと検査

```bash
# ホストで実行する（開発コンテナの作業ツリーでは npm ci しない。下の注意）
cd frontend
npm ci
npm test            # vitest（jsdom）。テストは src/ に同居（*.test.ts / *.test.tsx）
npm run build
npm run typecheck   # build の後（.next/types を使う）
```

- `vitest.config.ts`: `environment: 'jsdom'`、`restoreMocks: true`、`src/test/setup.ts`。実 API・ネットワークを呼ばない。`fetch` は `vi.spyOn(globalThis, 'fetch')`、`next/headers` と `next/navigation` は `vi.mock` で差し替える。
- `node_modules` と `.next` はホストと Compose のコンテナで分けている（ネイティブの依存が OS ごとに違うため）。開発コンテナからこのフォルダーで `npm ci` すると、ホストの `node_modules` を上書きしうる。別の場所にコピーして確かめる。
- lint（eslint など）は未導入。
