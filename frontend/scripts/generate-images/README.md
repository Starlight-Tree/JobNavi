# 画像の一括生成（管理用）

星庭ジョブの画像 27 枚（トップのヒーロー、求人詳細と園のバナーのサンプル各 1 枚、求人ごとの画像 24 枚＝`recruits/` 配下）を、Gemini の Batch API で事前に生成して保存する。
ページを開くたびには生成しない。生成はこのスクリプトからだけ行う。

- モデル: `gemini-nano-banana-2.1`（1K・16:9）
- 保存先: `frontend/public/images/generated/`（`<Image src="/images/generated/…">` で表示）
- 課金される。追加の生成は、事前に承認を得てから行う。

## 準備

`frontend/` で `npm ci` の後、キーを `scripts/generate-images/.env` に置く（Git 管理外）。

```
GEMINI_API_KEY=...
```

環境変数で渡してもよい。キーはログにも出さない（例外の文面に混ざっても伏せる）。

## 手順

```
# 送る内容だけ確認（API を呼ばない・課金なし）
npm run images:create-batch -- --all --dry-run

# 1 枚だけで、Batch が画像を返すかを確かめる
npm run images:create-batch -- --only top-hero
npm run images:check-batch            # 30 秒ごとに状態を確認（--once で 1 回だけ）
npm run images:download-results

# Batch を使わず、Standard API（interactions.create）で 1 枚ずつ生成する場合
npm run images:generate-single -- --id job-detail-sample

# 全件（27 枚。`recruits/` 配下の 24 枚も作り直す）。課金される Batch を作る。
# 作成の前に件数の表示も確認もない（件数は作成後に出る）。承認を得て、`--all --dry-run` で枚数を確かめてから実行する。
npm run images:create-batch -- --all
```

`download-results` は、同じ名前のファイルがあっても確かめずに上書きする。`--all` で上書きされるのは、今ある画像（直下の 2 枚と `recruits/` の 24 枚）のうち、返ってきた形式が JPEG のもの。
保存名は `outputFileName` の拡張子を、返ってきた形式の拡張子に替えて決める。`outputFileName` は `.png`、Git にある実ファイルはすべて `.jpg` なので、PNG が返ると `.jpg` が残ったまま同名の `.png` が増える（どちらが返るかは未確認）。
`recruits/` の画像を生成し直したら、`backend/demo_images.json` の `sha256` も同じ変更で更新する（[frontend/CLAUDE.md](../../CLAUDE.md)）。一致しない画像は `seed-images` が使わない。

作成したジョブ名は `.last-batch-job.txt`（Git 管理外）に残り、`check-batch` と `download-results` が読む。

## 画像を足す

`prompts.ts` の `IMAGES` に `id` `purpose` `prompt` `aspectRatio` `imageSize` `outputFileName` を足す。
リクエストの `metadata.key` が `id` で、結果の保存名に対応する。

## 生成後の確認

- 16:9 か、1K 相当か（保存時に PNG / JPEG の寸法を表示する）
- 文字・ロゴ・実在の人物・施設・商標が入っていないか
- 世界観が揃っているか

確認前の画像はコミットしない。

## 未検証

- 結果ファイル（JSONL）経由の取得（`download-results.ts` の `fileName` 分岐）は、実際には試していない。
- `generate-single.ts`（Standard API）は型検査と `--dry-run` だけ確認済みで、実際の呼び出しは未実施。`response_modalities` は SDK 上 deprecated 表記のため、実機で挙動を確かめる。
