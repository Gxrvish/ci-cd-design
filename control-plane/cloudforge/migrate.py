"""Apply the SQL files in control-plane/migrations, in order, each once (ADR-0014).

Run as `python -m cloudforge.migrate`. Applied files are immutable: a changed checksum stops the run.
"""

import hashlib
from pathlib import Path

import psycopg

from cloudforge.db import owner_conninfo

MIGRATIONS = Path(__file__).resolve().parent.parent / "migrations"
LOCK_ID = 1  # session advisory lock; only this runner takes advisory locks

# Its own schema, so the app role's grants on every table in `public` (0001) never cover it.
CREATE_LEDGER = """
    CREATE SCHEMA IF NOT EXISTS migration;
    CREATE TABLE IF NOT EXISTS migration.schema_migration (
        version text PRIMARY KEY,
        sha256 text NOT NULL,
        applied_at timestamptz NOT NULL DEFAULT now()
    )
"""


def migrate(conn: psycopg.Connection, directory: Path = MIGRATIONS) -> list[str]:
    """Apply pending files, one transaction each, and return their versions. `conn` must be in autocommit mode."""
    conn.execute("SELECT pg_advisory_lock(%s)", (LOCK_ID,))
    try:
        conn.execute(CREATE_LEDGER)
        applied = dict(conn.execute("SELECT version, sha256 FROM migration.schema_migration").fetchall())
        done = []
        for path in sorted(directory.glob("[0-9][0-9][0-9][0-9]_*.sql")):
            script = path.read_bytes()
            version, digest = path.stem, hashlib.sha256(script).hexdigest()
            if version in applied:
                if applied[version] != digest:
                    raise RuntimeError(f"{path.name} changed after it was applied")
                continue
            with conn.transaction():
                conn.execute(script.decode())
                conn.execute(
                    "INSERT INTO migration.schema_migration (version, sha256) VALUES (%s, %s)", (version, digest)
                )
            done.append(version)
        return done
    finally:
        conn.execute("SELECT pg_advisory_unlock(%s)", (LOCK_ID,))


if __name__ == "__main__":
    with psycopg.connect(owner_conninfo(), autocommit=True) as connection:
        versions = migrate(connection)
    print("applied:", ", ".join(versions) if versions else "nothing, up to date")
