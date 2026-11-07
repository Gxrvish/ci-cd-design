"""GET /healthz: 200 when the database answers, 503 when it doesn't. Compose's healthcheck relies on both."""

from fastapi.testclient import TestClient

from cloudforge.api import app


def test_ok_when_the_database_answers():
    with TestClient(app) as client:
        response = client.get("/healthz")

    assert (response.status_code, response.json()) == (200, {"status": "ok"})


def test_503_when_it_does_not(monkeypatch):
    monkeypatch.setenv("PGPORT", "1")  # nothing listens there
    with TestClient(app) as client:
        response = client.get("/healthz")

    assert (response.status_code, response.json()) == (503, {"detail": "database unavailable"})
