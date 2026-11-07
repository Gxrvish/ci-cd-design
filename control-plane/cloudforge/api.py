"""The control-plane HTTP API (ADR-0001): the FastAPI app that uvicorn serves (control-plane/Dockerfile).

It owns the process-wide database pool and mounts two routers: `dashboard` (/api, for people and scripts) and
`node_api` (/v1, for runners). Nginx forwards cloudforge-console.localhost to it (infrastructure/nginx/console.conf).

Async, because SSE log streams (M4, ADR-0006) keep many connections open and idle.
"""

from contextlib import asynccontextmanager

import psycopg
from fastapi import FastAPI, HTTPException
from psycopg_pool import AsyncConnectionPool

from cloudforge import dashboard, node_api
from cloudforge.db import app_conninfo


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Open the connection pool when the process starts, and close it when the process stops.

    FastAPI runs the code before `yield` at startup and the code after it at shutdown. Each request then borrows
    a connection from `app.state.pool` (db.Connection). Autocommit: every statement commits on its own, unless the
    code groups statements in `conn.transaction()` (ADR-0014).
    """
    async with AsyncConnectionPool(app_conninfo(), open=False, kwargs={"autocommit": True}) as pool:
        app.state.pool = pool
        yield


# The schema is served as JSON under /api (docs/api/dashboard.md). The /docs and /redoc pages are off: use Postman.
app = FastAPI(lifespan=lifespan, openapi_url="/api/openapi.json", docs_url=None, redoc_url=None)
app.include_router(dashboard.router)
app.include_router(node_api.router)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    """200 when the database answers, 503 otherwise (docs/api/dashboard.md).

    Compose's healthcheck calls it, so Nginx starts only once the API can reach PostgreSQL.
    """
    try:
        async with app.state.pool.connection(timeout=2) as conn:
            await conn.execute("SELECT 1")
    except psycopg.OperationalError as error:  # includes the pool's timeout
        raise HTTPException(503, "database unavailable") from error
    return {"status": "ok"}
