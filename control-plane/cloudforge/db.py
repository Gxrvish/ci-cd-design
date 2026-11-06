"""Database connection settings. libpq reads PGHOST and PGDATABASE from the environment (docker-compose.yml)."""

from collections.abc import AsyncIterator
from pathlib import Path
from typing import Annotated, Any

from fastapi import Depends, Request
from psycopg import AsyncConnection
from psycopg.conninfo import make_conninfo
from psycopg.rows import class_row

SECRETS = Path("/run/secrets")


def owner_conninfo() -> str:
    """The role that owns the schema. Only migrations use it (ADR-0014)."""
    return _conninfo("cloudforge_owner", "pg_owner_password")


def app_conninfo() -> str:
    """The role of the api and the worker: no DDL, and history tables are insert-only (schema grants)."""
    return _conninfo("cloudforge_app", "pg_app_password")


def _conninfo(user: str, secret: str) -> str:
    return make_conninfo(user=user, password=(SECRETS / secret).read_text().strip())


async def _connection(request: Request) -> AsyncIterator[AsyncConnection]:
    async with request.app.state.pool.connection() as conn:
        yield conn


# A pooled connection for one request. Autocommit: write several statements inside `conn.transaction()`.
Connection = Annotated[AsyncConnection, Depends(_connection)]


async def fetch_one[T](conn: AsyncConnection, row: type[T], query: str, params: dict[str, Any]) -> T | None:
    """Run `query` and return its first row as a `row` dataclass (ADR-0014), or None."""
    cursor = conn.cursor(row_factory=class_row(row))
    await cursor.execute(query, params)
    return await cursor.fetchone()
