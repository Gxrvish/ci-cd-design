"""The migration runner (cloudforge/migrate.py): order, only once, checksums, rollback and the lock.

Each test writes its own migration files into `tmp_path`, a temporary folder from pytest. The last test applies
the real ones.
"""

import psycopg
import pytest

from cloudforge.db import owner_conninfo
from cloudforge.migrate import LOCK_ID, migrate


def test_applies_pending_files_in_order_and_only_once(owner, tmp_path):
    (tmp_path / "0002_b.sql").write_text("ALTER TABLE t ADD COLUMN b int;")
    (tmp_path / "0001_a.sql").write_text("CREATE TABLE t (a int);")
    (tmp_path / "notes.sql").write_text("this is not a migration")

    assert migrate(owner, tmp_path) == ["0001_a", "0002_b"]
    assert migrate(owner, tmp_path) == []


def test_refuses_a_file_that_changed_after_it_was_applied(owner, tmp_path):
    (tmp_path / "0001_a.sql").write_text("CREATE TABLE t (a int);")
    migrate(owner, tmp_path)
    (tmp_path / "0001_a.sql").write_text("CREATE TABLE t (a bigint);")

    with pytest.raises(RuntimeError, match="0001_a.sql changed after it was applied"):
        migrate(owner, tmp_path)


def test_a_failing_file_leaves_no_trace(owner, tmp_path):
    (tmp_path / "0001_a.sql").write_text("CREATE TABLE t (a int); SELECT 1 / 0;")

    with pytest.raises(psycopg.errors.DivisionByZero):
        migrate(owner, tmp_path)
    assert owner.execute("SELECT to_regclass('t')").fetchone() == (None,)
    assert owner.execute("SELECT count(*) FROM migration.schema_migration").fetchone() == (0,)


def test_waits_for_another_runner(owner, tmp_path):
    with psycopg.connect(owner_conninfo(), autocommit=True) as other:
        other.execute("SELECT pg_advisory_lock(%s)", (LOCK_ID,))
        owner.execute("SET lock_timeout = '200ms'")

        with pytest.raises(psycopg.errors.LockNotAvailable):
            migrate(owner, tmp_path)


def test_the_shipped_migrations_apply(owner):
    assert migrate(owner) == ["0001_initial", "0002_default_organization"]
