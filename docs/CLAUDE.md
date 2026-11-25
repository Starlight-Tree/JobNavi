# docs/（文書）

全体の方針・禁止事項は、ルートの [CLAUDE.md](../CLAUDE.md)。文書を触るときの規則は [.claude/rules/docs.md](../.claude/rules/docs.md)。

## どの文書が何の正本か

| 知りたいこと | 文書 |
|---|---|
| 画像生成（Vertex AI のジョブ）の、デプロイ済みの設定・成功した実行・モデル・保存先・完了条件 | [image-gen-resources.md](image-gen-resources.md)（**台帳。正本**） |
| 台帳の根拠になる、確認の出力の置き場（中身の状況は、その README と台帳） | [image-gen-evidence/](image-gen-evidence/README.md) |
| 求人・法人の画像の保存の仕組み（オブジェクトストレージ・DB・バックアップ・マイグレーション）。画像生成ジョブの出力先は台帳 | [image-storage.md](image-storage.md) |
| 資格情報・権限・MCP をプロジェクトごとに分ける方針と、その現状 | [access-separation.md](access-separation.md) |
| ローカルの開発・テスト・検査・ダミーデータ・CI | [development.md](development.md) |
| API を Python から Go に置き換えた経緯・範囲・確かめ方・意図して違う点・未確認 | [go-migration.md](go-migration.md) |
| 求人検索画面の調査の記録（以降の調査は追記） | [ux-research.md](ux-research.md) |
| 旧システム（PHP）から残す参考 | [legacy-reference.md](legacy-reference.md) |
| 2026-11-15 の履歴の書き換えと、古いハッシュの対応表 | [history-rewrite.md](history-rewrite.md) |

## 決定の記録

決定とその日付は、関係する文書に書く。画像生成・保存先は台帳、権限・資格情報は `access-separation.md`、保留中の方針はルートの [CLAUDE.md](../CLAUDE.md) の「保留中」。
同じ決定は 1 か所に書き、他の文書からは節名で指す。決定が増えて探しにくくなったら、`docs/decisions/` に 1 件 1 ファイルで置くことを、別に提案する。
