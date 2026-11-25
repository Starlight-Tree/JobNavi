---
paths:
  - "frontend/**"
---

# frontend/ を触るとき

詳細は [frontend/CLAUDE.md](../../frontend/CLAUDE.md)。

- API はサーバー側から呼ぶ。トークンは HttpOnly の Cookie だけ。`NEXT_PUBLIC_*` に秘密を入れない
- `next.config.ts` の `agentRules: false`、`remotePatterns`（配信元の `images/` だけ）を広げない・外さない
- `public/images/generated/recruits/` の画像を変えたら、`backend/demo_images.json` の `sha256` も同じ変更で更新する
- `frontend/scripts/generate-images/` の実行は課金される。承認を得てから。`--dry-run` は課金なし
- `node_modules` と `.next` はホストと分けてある。この作業ツリーで `npm ci` を実行しない（別の場所にコピーして確かめる）
- 変更したら `npm test` → `npm run build` → `npm run typecheck`（この順）を実行してから報告する
