"""Apply the SQL files in control-plane/migrations, in order, each once (ADR-0014).

Run as `python -m cloudforge.migrate`: the migrate service does it before the api starts (docker-compose.yml).
It connects as cloudforge_owner, the only role that may change the schema.

Applied files are immutable: the ledger keeps each file's SHA-256, and a changed checksum stops the run.
Fix a mistake with a new file, never by editing an applied one.
"""

import hashlib
from pathlib import Path

import psycopg

from cloudforge.db import owner_conninfo

MIGRATIONS = Path(__file__).resolve().parent.parent / "migrations"
# A session advisory lock locks a number, not a row: two migrate runs at once take turns instead of racing.
# Only this runner takes advisory locks, so the number can't clash with anyone else's.
LOCK_ID = 1

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
    """Apply pending files, one transaction each, and return their versions. `conn` must be in autocommit mode.

    Autocommit makes each `conn.transaction()` a real transaction that commits its file, not a savepoint inside
    one big transaction. A version is the file name without `.sql`, such as `0001_initial`. A failing file
    rolls back alone and stops the run; the files before it stay applied.

    Raises RuntimeError when an applied file has changed since.
    """
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
