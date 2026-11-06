"""Tests run against the `cloudforge_test` database (PGDATABASE, docker-compose.yml)."""

import psycopg
import pytest
from fastapi.testclient import TestClient

from cloudforge.api import app
from cloudforge.db import owner_conninfo
from cloudforge.migrate import migrate


@pytest.fixture
def owner():
    """An owner connection to an empty database: everything the owner created before is dropped."""
    with psycopg.connect(owner_conninfo(), autocommit=True) as conn:
        conn.execute("DROP OWNED BY CURRENT_USER")
        yield conn


@pytest.fixture
def client(owner):
    """The API on a freshly migrated database."""
    migrate(owner)
    with TestClient(app) as client:
        yield client
