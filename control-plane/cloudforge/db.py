"""Database access: connection settings, one pooled connection per request, and rows as dataclasses.

libpq, the C library under psycopg, reads PGHOST and PGDATABASE from the environment (docker-compose.yml).
The passwords are Compose secrets, mounted as files in /run/secrets. No ORM (ADR-0014): the SQL is written by
hand, with bound parameters, and each query names the dataclass its rows become.
"""

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
    """A libpq connection string for `user`, with the password read from the secret file named `secret`."""
    return make_conninfo(user=user, password=(SECRETS / secret).read_text().strip())


async def _connection(request: Request) -> AsyncIterator[AsyncConnection]:
    """Borrow a connection from the pool (api.lifespan) for one request, and give it back afterwards.

    FastAPI runs this as a dependency: the code before `yield` runs before the route function, and the rest
    once the request is finished, like a `with` block around the route.
    """
    async with request.app.state.pool.connection() as conn:
        yield conn


# Dependency injection: a route parameter `conn: Connection` makes FastAPI call `_connection` and pass in what it
# yields. Autocommit: write several statements inside `conn.transaction()`.
Connection = Annotated[AsyncConnection, Depends(_connection)]


async def fetch_one[T](conn: AsyncConnection, row: type[T], query: str, params: dict[str, Any]) -> T | None:
    """Run `query` and return its first row as a `row` dataclass (ADR-0014), or None when there is no row.

    `params` fills the query's `%(name)s` placeholders. psycopg sends the values apart from the SQL text, so a value
    can never change the query. The selected column names must match the dataclass's fields. `[T]` makes the
    function generic: it returns an instance of whichever class `row` is.
    """
    cursor = conn.cursor(row_factory=class_row(row))
    await cursor.execute(query, params)
    return await cursor.fetchone()
