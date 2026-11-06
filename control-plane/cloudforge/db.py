"""Database connection settings. libpq reads PGHOST and PGDATABASE from the environment (docker-compose.yml)."""

from pathlib import Path

from psycopg.conninfo import make_conninfo

SECRETS = Path("/run/secrets")


def owner_conninfo() -> str:
    """The role that owns the schema. Only migrations use it (ADR-0014)."""
    return _conninfo("cloudforge_owner", "pg_owner_password")


def app_conninfo() -> str:
    """The role of the api and the worker: no DDL, and history tables are insert-only (schema grants)."""
    return _conninfo("cloudforge_app", "pg_app_password")


def _conninfo(user: str, secret: str) -> str:
    return make_conninfo(user=user, password=(SECRETS / secret).read_text().strip())
