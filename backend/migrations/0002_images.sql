-- 画像の管理（求人・法人の画像）。既存の表にはデータを変えずに、列を 1 つ足すだけ（recruits.demo_key。NULL 可・既定値なし）。
-- ロック: recruits への ALTER は一瞬の ACCESS EXCLUSIVE（列の追加は表を書き換えない）。ほかは新しい表の作成だけ。
-- recruits.formfile（「画像ファイル」の列）は触らない。値と使用箇所の確認が済むまで、列も API の項目も残す。

-- 画像の本体は、オブジェクトストレージの storage_key に置く。DB には、キーと属性だけを持つ（署名付き URL は保存しない）。
-- status: uploading（登録の途中）→ pending（検証済み・非公開側だけにある）→ publishing（公開側へ複製中）→ approved（公開してよい）。
--         rejected（却下）/ deleted（実体を消した。行は記録として残す）。
CREATE TABLE images (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  storage_key             text NOT NULL UNIQUE,    -- images/<id>.<拡張子>。更新は同じキーに上書きせず、新しい画像として登録する
  mime_type               text NOT NULL CHECK (mime_type IN ('image/jpeg', 'image/png', 'image/webp')),
  byte_size               integer NOT NULL CHECK (byte_size > 0),
  width                   integer NOT NULL CHECK (width > 0),
  height                  integer NOT NULL CHECK (height > 0),
  sha256                  char(64) NOT NULL,       -- 配信用（メタデータ除去・再エンコード後）のバイト列の sha256
  alt                     text NOT NULL CHECK (char_length(alt) BETWEEN 1 AND 300),
  origin                  text NOT NULL CHECK (origin IN ('generated', 'uploaded')),   -- 画像生成（運営）/ 園のアップロード
  owner_garden_account_id integer REFERENCES garden_accounts(id) ON DELETE SET NULL,   -- NULL は運営の管理
  demo_key                text UNIQUE,             -- ダミーデータの画像を、再実行しても重複させずに結ぶための固定のキー
  status                  text NOT NULL CHECK (status IN ('uploading', 'pending', 'publishing', 'approved', 'rejected', 'deleted')),
  created_at              timestamptz NOT NULL DEFAULT now(),
  status_changed_at       timestamptz NOT NULL DEFAULT now(),
  approved_at             timestamptz,
  deletion_requested_at   timestamptz,             -- 園が外した・差し替えた時刻。猶予のあとに掃除の対象になる
  deleted_at              timestamptz,
  CHECK (status <> 'approved' OR approved_at IS NOT NULL)
);
CREATE INDEX images_status ON images (status, status_changed_at);
CREATE INDEX images_owner ON images (owner_garden_account_id);

-- 求人と画像（複数枚・表示順）。参照されている画像の行は消せない（RESTRICT）。
CREATE TABLE recruit_images (
  recruit_id  integer NOT NULL REFERENCES recruits(id) ON DELETE CASCADE,
  image_id    uuid    NOT NULL REFERENCES images(id) ON DELETE RESTRICT,
  position    integer NOT NULL CHECK (position >= 0),
  is_hero     boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (recruit_id, image_id),
  UNIQUE (recruit_id, position) DEFERRABLE INITIALLY DEFERRED   -- 並べ替えの途中で一時的に重なってもよい（コミット時に検査）
);
-- ヒーローは 1 求人に 1 枚まで
CREATE UNIQUE INDEX recruit_images_one_hero ON recruit_images (recruit_id) WHERE is_hero;
CREATE INDEX recruit_images_image ON recruit_images (image_id);

-- 法人の画像（ロゴ・バナー）。同じ images を使う。
CREATE TABLE garden_images (
  garden_account_id integer NOT NULL REFERENCES garden_accounts(id) ON DELETE CASCADE,
  image_id          uuid    NOT NULL REFERENCES images(id) ON DELETE RESTRICT,
  role              text    NOT NULL CHECK (role IN ('logo', 'banner')),
  position          integer NOT NULL CHECK (position >= 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (garden_account_id, image_id),
  UNIQUE (garden_account_id, role, position) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX garden_images_image ON garden_images (image_id);

-- ヒーローにできるのは、公開してよい（approved）画像だけ。アプリの検査に加えて、DB でも守る。
CREATE FUNCTION recruit_images_hero_must_be_approved() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.is_hero AND NOT EXISTS (SELECT 1 FROM images WHERE id = NEW.image_id AND status = 'approved') THEN
    RAISE EXCEPTION 'ヒーロー画像は、承認済みの画像だけです' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER recruit_images_hero_check BEFORE INSERT OR UPDATE OF is_hero, image_id ON recruit_images
  FOR EACH ROW EXECUTE FUNCTION recruit_images_hero_must_be_approved();

-- 画像が approved でなくなったら（却下・削除・公開の取り消し）、ヒーローの印を外す。
CREATE FUNCTION images_clear_hero_when_unpublished() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status <> 'approved' THEN
    UPDATE recruit_images SET is_hero = false WHERE image_id = NEW.id AND is_hero;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER images_status_clear_hero AFTER UPDATE OF status ON images
  FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION images_clear_hero_when_unpublished();

-- ダミーデータの求人を、法人名＋職種名ではなく固定のキーで識別する（seed が入れる。既存の行は seed_demo が一度だけ結ぶ）。
ALTER TABLE recruits ADD COLUMN demo_key text;
CREATE UNIQUE INDEX recruits_demo_key ON recruits (demo_key) WHERE demo_key IS NOT NULL;
