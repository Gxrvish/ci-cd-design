"""Shared pytest fixtures. Tests run against the `cloudforge_test` database (PGDATABASE, docker-compose.yml).

A fixture is a function that pytest runs for each test that names it as a parameter: `def test_x(client):` gets
what `client` yields, and the code after `yield` runs when the test ends. A fixture can use other fixtures.
"""

import psycopg
import pytest
from fastapi.testclient import TestClient

from cloudforge.api import app
from cloudforge.db import owner_conninfo
from cloudforge.migrate import migrate
from cloudforge.seed import seed


@pytest.fixture
def owner():
    """An owner connection to an empty database: everything the owner created before is dropped.

    `DROP OWNED BY` removes every table, type and schema the role created, so each test starts from nothing.
    """
    with psycopg.connect(owner_conninfo(), autocommit=True) as conn:
        conn.execute("DROP OWNED BY CURRENT_USER")
        yield conn


@pytest.fixture
def client(owner):
    """The API on a freshly migrated and seeded database.

    TestClient calls the app in-process, with no server and no network. It also runs the app's lifespan, so each
    test opens and closes its own connection pool.
    """
    migrate(owner)
    seed(owner)
    with TestClient(app) as client:
        yield client
