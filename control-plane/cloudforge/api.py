"""The control-plane HTTP API (ADR-0001).

Async, because SSE log streams (M4, ADR-0006) keep many connections open and idle.
"""

from contextlib import asynccontextmanager

import psycopg
from fastapi import FastAPI, HTTPException
from psycopg_pool import AsyncConnectionPool

from cloudforge import dashboard
from cloudforge.db import app_conninfo


@asynccontextmanager
async def lifespan(app: FastAPI):
    async with AsyncConnectionPool(app_conninfo(), open=False, kwargs={"autocommit": True}) as pool:
        app.state.pool = pool
        yield


app = FastAPI(lifespan=lifespan, openapi_url="/api/openapi.json", docs_url=None, redoc_url=None)
app.include_router(dashboard.router)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    """200 when the database answers, 503 otherwise (docs/api/dashboard.md)."""
    try:
        async with app.state.pool.connection(timeout=2) as conn:
            await conn.execute("SELECT 1")
    except psycopg.OperationalError as error:  # includes the pool's timeout
        raise HTTPException(503, "database unavailable") from error
    return {"status": "ok"}
