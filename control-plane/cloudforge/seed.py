"""Apply the SQL files in control-plane/seeds, in order, on every start (ADR-0014, "Data kinds").

Seeds hold what every installation needs before anyone can use it, such as the organization `default`.
Unlike migrations they keep no ledger: each file must be safe to run again (INSERT ... ON CONFLICT DO NOTHING),
so a seed can change or go away later without a new migration. Demo data is never seeded: it goes through the API.

Run as `python -m cloudforge.seed`: the migrate service does it right after the migrations (docker-compose.yml).
"""

from pathlib import Path

import psycopg

from cloudforge.db import owner_conninfo

SEEDS = Path(__file__).resolve().parent.parent / "seeds"


def seed(conn: psycopg.Connection, directory: Path = SEEDS) -> list[str]:
    """Apply every seed file, in name order, one transaction each, and return their names.

    `conn` must be in autocommit mode, so that each file commits on its own, as in migrate.
    """
    names = []
    for path in sorted(directory.glob("[0-9][0-9][0-9][0-9]_*.sql")):
        with conn.transaction():
            conn.execute(path.read_text())
        names.append(path.stem)
    return names


if __name__ == "__main__":
    with psycopg.connect(owner_conninfo(), autocommit=True) as connection:
        print("seeded:", ", ".join(seed(connection)) or "nothing")
