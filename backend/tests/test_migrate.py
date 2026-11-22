import hashlib
import json
import shutil

import psycopg
import pytest

from app import migrate
from tests.conftest import TEST_URL, load_initial_schema, new_database

pytestmark = pytest.mark.skipif(not TEST_URL, reason="TEST_DATABASE_URL が未設定です")

# 今ある番号。マイグレーションを足したら、ここだけ直す（0001 は baseline）
ALL_VERSIONS = ["0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009"]


def run(url: str, **kw) -> tuple[int, list[str]]:
    lines: list[str] = []
    code = migrate.apply(url, out=lines.append, **kw)
    return code, lines


def table_exists(url: str, name: str) -> bool:
    with psycopg.connect(url) as conn:
        return conn.execute("SELECT to_regclass(%s)", (f"public.{name}",)).fetchone()[0] is not None


def test_committed_fingerprint_matches_a_fresh_initial_schema():
    url = new_database("hj_mig_fp")
    load_initial_schema(url)
    with psycopg.connect(url) as conn:
        actual = migrate.fingerprint(conn)
    assert actual == json.loads(migrate.FINGERPRINT_FILE.read_text(encoding="utf-8"))["lines"]


def test_new_database_gets_baseline_then_all_migrations_and_rerun_is_a_noop():
    url = new_database("hj_mig_new")
    load_initial_schema(url)
    code, lines = run(url)
    assert code == 0
    with psycopg.connect(url) as conn:
        versions = [r[0] for r in conn.execute("SELECT version FROM schema_migrations ORDER BY version")]
    assert versions == ALL_VERSIONS
    assert table_exists(url, "images") and table_exists(url, "recruit_images") and table_exists(url, "garden_images")
    assert table_exists(url, "recruit_events") and table_exists(url, "recruit_embeddings")
    assert table_exists(url, "garden_login_attempts")
    code, lines = run(url)
    assert code == 0 and any("未適用: なし" in line for line in lines)


def test_garden_login_attempts_table_only_accepts_a_sha256_hash():
    """園ログインの試行の表（0007）には、生のメールアドレスを入れられない（SHA-256 の 16 進 64 文字だけ）。"""
    url = new_database("hj_mig_attempts")
    load_initial_schema(url)
    assert run(url)[0] == 0
    good = hashlib.sha256(b"a@example.com").hexdigest()
    with psycopg.connect(url, autocommit=True) as conn:
        conn.execute("INSERT INTO garden_login_attempts (email_hash, failures) VALUES (%s, 1)", (good,))
        for bad in ("a@example.com", good.upper(), good[:-1], ""):
            with pytest.raises(psycopg.errors.CheckViolation):
                conn.execute("INSERT INTO garden_login_attempts (email_hash, failures) VALUES (%s, 1)", (bad,))
        with pytest.raises(psycopg.errors.CheckViolation):
            conn.execute("UPDATE garden_login_attempts SET failures = 0 WHERE email_hash = %s", (good,))
        columns = [r[0] for r in conn.execute("SELECT column_name FROM information_schema.columns WHERE table_name = 'garden_login_attempts'")]
    assert sorted(columns) == ["email_hash", "failures", "window_start"]  # メールを入れる列は無い


def test_display_name_is_backfilled_from_the_old_name_columns():
    url = new_database("hj_mig_dispname")
    load_initial_schema(url)
    with psycopg.connect(url, autocommit=True) as conn:
        conn.execute(
            "INSERT INTO users (last_name, first_name, email, password_hash) VALUES "
            "('苔野', 'ひかり', 'a@example.com', 'h'), (NULL, NULL, 'b@example.com', 'h'), ('長' || repeat('い', 60), '名', 'c@example.com', 'h')"
        )
    assert run(url)[0] == 0
    with psycopg.connect(url) as conn:
        rows = dict(conn.execute("SELECT email, display_name FROM users").fetchall())
        with pytest.raises(psycopg.errors.CheckViolation):
            conn.execute("UPDATE users SET display_name = '' WHERE email = 'a@example.com'")
    assert rows["a@example.com"] == "苔野 ひかり"  # 姓と名を空白でつなぐ
    assert rows["b@example.com"] is None  # どちらも空なら NULL のまま
    assert len(rows["c@example.com"]) == 30  # 上限（30 文字）までで切る


def test_existing_data_is_preserved_and_ids_do_not_change():
    url = new_database("hj_mig_existing")
    load_initial_schema(url)
    with psycopg.connect(url, autocommit=True) as conn:
        for i in range(1, 6):
            conn.execute(
                "INSERT INTO recruits (category, corporate, formfile, is_displayed) VALUES (%s, %s, %s, true)", (f"職種{i}", f"法人{i}", "old.jpg" if i == 3 else None)
            )
        conn.execute("INSERT INTO recruit_areas VALUES (2, 3)")
        conn.execute("INSERT INTO users (last_name, first_name, email, password_hash) VALUES ('a', 'b', 'x@example.com', 'h')")

    def snapshot() -> str:
        with psycopg.connect(url) as conn:
            rows = conn.execute("SELECT id, category, corporate, formfile, is_displayed, created_at FROM recruits ORDER BY id").fetchall()
            users = conn.execute("SELECT id, email FROM users ORDER BY id").fetchall()
            areas = conn.execute("SELECT * FROM recruit_areas ORDER BY 1, 2").fetchall()
        return hashlib.sha256(repr((rows, users, areas)).encode()).hexdigest()

    before = snapshot()
    assert run(url)[0] == 0
    assert snapshot() == before  # 既存の行・ID・formfile は変わらない
    with psycopg.connect(url) as conn:
        assert conn.execute("SELECT count(*) FROM recruits WHERE demo_key IS NOT NULL").fetchone()[0] == 0  # 列を足しただけ


@pytest.mark.parametrize(
    "sabotage",
    ["ALTER TABLE recruits DROP COLUMN note3", "ALTER TABLE recruits ADD COLUMN extra text", "DROP INDEX recruits_garden", "DELETE FROM areas WHERE code = (SELECT min(code) FROM areas)"],
)
def test_unexpected_schema_is_not_recorded_as_applied_and_nothing_is_changed(sabotage):
    url = new_database("hj_mig_bad")
    load_initial_schema(url)
    with psycopg.connect(url, autocommit=True) as conn:
        try:
            conn.execute(sabotage)
        except psycopg.errors.DependentObjectsStillExist:
            conn.execute("DROP VIEW recruit_search")
            conn.execute(sabotage)
    code, lines = run(url)
    assert code == 1 and any("一致しません" in line for line in lines)
    assert not table_exists(url, "schema_migrations") and not table_exists(url, "images")


def test_database_without_initial_schema_stops():
    url = new_database("hj_mig_empty")
    code, lines = run(url)
    assert code == 1 and any("初期スキーマがありません" in line for line in lines)


def test_dry_run_changes_nothing():
    url = new_database("hj_mig_dry")
    load_initial_schema(url)
    code, lines = run(url, dry_run=True)
    assert code == 0 and any("0002_images" in line for line in lines)
    assert not table_exists(url, "schema_migrations") and not table_exists(url, "images")


def test_edited_applied_migration_is_detected(tmp_path, monkeypatch):
    url = new_database("hj_mig_sum")
    load_initial_schema(url)
    work = tmp_path / "migrations"
    shutil.copytree(migrate.MIGRATIONS_DIR, work)
    monkeypatch.setattr(migrate, "MIGRATIONS_DIR", work)
    monkeypatch.setattr(migrate, "FINGERPRINT_FILE", work / "baseline_fingerprint.json")
    assert run(url)[0] == 0
    (work / "0002_images.sql").write_text((work / "0002_images.sql").read_text(encoding="utf-8") + "\n-- 書き換え\n", encoding="utf-8")
    code, lines = run(url)
    assert code == 1 and any("書き換えられています" in line for line in lines)


def test_failed_migration_rolls_back_and_is_not_recorded(tmp_path, monkeypatch):
    url = new_database("hj_mig_fail")
    load_initial_schema(url)
    work = tmp_path / "migrations"
    shutil.copytree(migrate.MIGRATIONS_DIR, work)
    next_version = f"{len(migrate.migration_files()) + 2:04d}"  # 今ある最後の番号の次（本物のマイグレーションと番号が重ならない）
    (work / f"{next_version}_broken.sql").write_text("CREATE TABLE half_done (id int);\nSELECT 1/0;\n", encoding="utf-8")
    monkeypatch.setattr(migrate, "MIGRATIONS_DIR", work)
    monkeypatch.setattr(migrate, "FINGERPRINT_FILE", work / "baseline_fingerprint.json")
    with pytest.raises(psycopg.errors.DivisionByZero):
        run(url)
    assert not table_exists(url, "half_done")  # 途中までの変更は残らない
    with psycopg.connect(url) as conn:
        assert [r[0] for r in conn.execute("SELECT version FROM schema_migrations ORDER BY 1")] == ALL_VERSIONS


def test_backup_and_restore_roundtrip(tmp_path):
    """README の復旧手順（pg_dump → 別の DB へ pg_restore）で、データが戻ることの確認。"""
    import os
    import subprocess

    url = new_database("hj_mig_backup")
    load_initial_schema(url)
    assert run(url)[0] == 0
    with psycopg.connect(url, autocommit=True) as conn:
        conn.execute("INSERT INTO recruits (category, corporate, is_displayed, demo_key) VALUES ('復旧', '確認', true, 'restore-check')")
    dump = tmp_path / "hj.dump"
    pg_bin = "/usr/lib/postgresql/17/bin"
    if not os.path.exists(f"{pg_bin}/pg_dump"):
        pytest.skip("pg_dump が見つかりません")
    subprocess.run([f"{pg_bin}/pg_dump", "-Fc", "-f", str(dump), url], check=True)
    restored = new_database("hj_mig_restored")
    subprocess.run([f"{pg_bin}/pg_restore", "--no-owner", "-d", restored, str(dump)], check=True)
    with psycopg.connect(restored) as conn:
        assert conn.execute("SELECT category FROM recruits WHERE demo_key = 'restore-check'").fetchone()[0] == "復旧"
        assert [r[0] for r in conn.execute("SELECT version FROM schema_migrations ORDER BY 1")] == ALL_VERSIONS
    assert run(restored)[0] == 0
