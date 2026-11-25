-- 表示名（ニックネーム）。利用者の表（users）に 1 列足すだけ。画面とAPI は、姓・名の 2 列（last_name・first_name）を使わず、この列を使う。
-- 姓・名の列は、DROP しない規則なので残す（応募の entries の姓・名とは別。そちらは変えない）。
-- ロック: users への ALTER は一瞬の ACCESS EXCLUSIVE（列の追加は表を書き換えない）。UPDATE は、表示名がまだ空の行だけ。

-- 1〜30 文字（アプリと同じ。文字数はコードポイント）。空の文字列は持たない（無いときは NULL。画面はメールアドレスを出す）。
ALTER TABLE users ADD COLUMN display_name text CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 30);

-- 既存の行は、姓と名を空白でつないだものを、30 文字までで引き継ぐ（どちらも空なら NULL のまま）。
UPDATE users
SET display_name = left(nullif(btrim(concat_ws(' ', last_name, first_name)), ''), 30)
WHERE display_name IS NULL;
