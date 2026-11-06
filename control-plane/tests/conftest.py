"""Tests run against the `cloudforge_test` database (PGDATABASE, docker-compose.yml)."""

import psycopg
import pytest

from cloudforge.db import owner_conninfo


@pytest.fixture
def owner():
    """An owner connection to an empty database: everything the owner created before is dropped."""
    with psycopg.connect(owner_conninfo(), autocommit=True) as conn:
        conn.execute("DROP OWNED BY CURRENT_USER")
        yield conn
