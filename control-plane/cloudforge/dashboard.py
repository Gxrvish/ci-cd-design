"""Dashboard API, the M1 part (docs/api/dashboard.md).

No login until M4: every caller acts for the seeded organization `default` (ADR-0015, docs/stubs.md).
The schema's constraints validate formats; their errors become 422 or 409 here.

Request bodies are Pydantic models: FastAPI checks the JSON against them and answers 422 when it doesn't fit.
Responses are dataclasses, filled from SQL rows whose columns have the same names (db.fetch_one).
A route's docstring is also its description in the API schema, /api/openapi.json.
"""

from collections.abc import Generator
from contextlib import contextmanager
from dataclasses import dataclass
from datetime import datetime
from typing import Literal

from fastapi import APIRouter, HTTPException
from fastapi.responses import PlainTextResponse
from psycopg import AsyncConnection
from psycopg.errors import CheckViolation, ForeignKeyViolation, UniqueViolation
from pydantic import BaseModel

from cloudforge.credentials import new_secret
from cloudforge.db import Connection, fetch_one

router = APIRouter(prefix="/api")


class NewRepository(BaseModel):
    """Body of POST /api/orgs/{org}/repositories."""

    provider: Literal["gitea"]  # GitHub arrives in M3
    external_id: str  # the provider's repository id; M3 checks it with the provider (docs/stubs.md)
    full_name: str


class NewProject(BaseModel):
    """Body of POST /api/orgs/{org}/projects. `root_dir` is the app's folder in the repository; "" is its root."""

    slug: str
    name: str
    repository_id: int
    production_branch: str = "main"
    root_dir: str = ""


class NewRunnerToken(BaseModel):
    """Body of POST /api/orgs/{org}/runner-tokens."""

    pool: Literal["trusted", "untrusted"]  # decides which builds its runner may claim (ADR-0009)


class NewBuild(BaseModel):
    """Body of POST /api/projects/{slug}/builds."""

    commit_sha: str  # builds the production branch's environment; other branches arrive in M7


@dataclass
class Repository:
    """A connected Git repository."""

    id: int
    provider: str
    external_id: str
    full_name: str
    created_at: datetime


@dataclass
class Project:
    """One deployable app: a folder of a repository, built and deployed on its own."""

    id: int
    slug: str
    name: str
    repository_id: int
    production_branch: str
    root_dir: str
    created_at: datetime


@dataclass
class Build:
    """A build of one commit, with its job's attempt count (0 until a runner claims it).

    `state` and `reason` follow docs/architecture/state-machines.md.
    """

    id: int
    project: str
    commit_sha: str
    trigger: str
    state: str
    reason: str | None
    image_digest: str | None
    attempt: int
    created_at: datetime
    started_at: datetime | None
    finished_at: datetime | None


@dataclass
class RunnerToken:
    """A one-time registration token. The database keeps only its hash, so this answer is the only copy."""

    token: str  # shown once
    expires_at: datetime


@dataclass
class Id:
    """A row's id, for queries that return nothing else."""

    id: int


@dataclass
class LogText:
    """An attempt's whole log, joined from its chunks."""

    text: str


@router.post("/orgs/{org}/repositories", status_code=201)
async def connect_repository(org: str, body: NewRepository, conn: Connection) -> Repository:
    """Connect a Git repository to the organization. 409 if it is connected already."""
    organization_id = await _organization_id(conn, org)
    with _constraint_errors():
        return await fetch_one(
            conn,
            Repository,
            """
            INSERT INTO git_repository (organization_id, provider, external_id, full_name)
            VALUES (%(organization_id)s, %(provider)s, %(external_id)s, %(full_name)s)
            RETURNING id, provider, external_id, full_name, created_at
            """,
            body.model_dump() | {"organization_id": organization_id},
        )


@router.post("/orgs/{org}/projects", status_code=201)
async def create_project(org: str, body: NewProject, conn: Connection) -> Project:
    """Create a project, with its production branch and that branch's environment.

    404 for an unknown repository, 409 for a slug in use, 422 when the schema refuses a value.
    """
    params = body.model_dump() | {"organization_id": await _organization_id(conn, org)}
    with _constraint_errors():
        async with conn.transaction():
            try:
                project = await fetch_one(
                    conn,
                    Project,
                    """
                    INSERT INTO project (organization_id, repository_id, slug, name, production_branch, root_dir)
                    VALUES (%(organization_id)s, %(repository_id)s, %(slug)s, %(name)s,
                            %(production_branch)s, %(root_dir)s)
                    RETURNING id, slug, name, repository_id, production_branch, root_dir, created_at
                    """,
                    params,
                )
            except ForeignKeyViolation as error:  # missing, or another organization's: 404 either way
                raise HTTPException(404, "repository not found") from error
            # Another project of the same repository may have created this branch row already.
            await conn.execute(
                "INSERT INTO branch (repository_id, name) VALUES (%(repository_id)s, %(production_branch)s)"
                " ON CONFLICT (repository_id, name) DO NOTHING",
                params,
            )
            await conn.execute(
                """
                INSERT INTO environment (project_id, kind, branch_id)
                SELECT %(project_id)s, 'production', id FROM branch
                WHERE repository_id = %(repository_id)s AND name = %(production_branch)s
                """,
                params | {"project_id": project.id},
            )
    return project


@router.post("/projects/{slug}/builds", status_code=201)
async def create_build(slug: str, body: NewBuild, conn: Connection) -> Build:
    """Queue a manual build of an exact commit, and its job. A runner picks the job up with POST /v1/jobs/claim.

    J1 and B1 in docs/architecture/state-machines.md, without event rows until M2. 404 for an unknown project.
    """
    with _constraint_errors():
        async with conn.transaction():
            # One statement finds the project's production environment and inserts the build: no row, no project.
            build = await fetch_one(
                conn,
                Id,
                """
                INSERT INTO build (project_id, repository_id, environment_id, commit_sha, trigger, trusted, state)
                SELECT p.id, p.repository_id, e.id, %(commit_sha)s, 'manual', true, 'queued'
                FROM project p JOIN environment e ON e.project_id = p.id AND e.kind = 'production'
                WHERE p.slug = %(slug)s
                RETURNING id
                """,
                {"slug": slug, "commit_sha": body.commit_sha},
            )
            if build is None:
                raise HTTPException(404, "project not found")
            await conn.execute("INSERT INTO job (build_id, state) VALUES (%(id)s, 'queued')", {"id": build.id})
    return await get_build(build.id, conn)


@router.post("/orgs/{org}/runner-tokens", status_code=201)
async def create_runner_token(org: str, body: NewRunnerToken, conn: Connection) -> RunnerToken:
    """A one-time token, valid for 1 hour, that registers one runner of this organization (R1).

    The runner sends it to POST /v1/runners/register. Its pool decides which builds the runner may claim.
    """
    token, token_hash = new_secret()  # only the hash reaches the database
    cursor = await conn.execute(
        """
        INSERT INTO registration_token (kind, organization_id, pool, token_hash, expires_at)
        VALUES ('runner', %(organization_id)s, %(pool)s, %(token_hash)s, now() + interval '1 hour')
        RETURNING expires_at
        """,
        {"organization_id": await _organization_id(conn, org), "pool": body.pool, "token_hash": token_hash},
    )
    (expires_at,) = await cursor.fetchone()
    return RunnerToken(token, expires_at)


@router.get("/builds/{build_id}")
async def get_build(build_id: int, conn: Connection) -> Build:
    """Read one build. 404 if there is none."""
    build = await fetch_one(
        conn,
        Build,
        """
        SELECT b.id, p.slug AS project, b.commit_sha, b.trigger, b.state, b.reason, b.image_digest,
               j.attempt, b.created_at, b.started_at, b.finished_at
        FROM build b JOIN project p ON p.id = b.project_id JOIN job j ON j.build_id = b.id
        WHERE b.id = %(id)s
        """,
        {"id": build_id},
    )
    if build is None:
        raise HTTPException(404, "build not found")
    return build


@router.get("/builds/{build_id}/attempts/{attempt}/log", response_class=PlainTextResponse)
async def read_log(build_id: int, attempt: int, conn: Connection) -> str:
    """One attempt's whole log, as text. M4 makes this path a resumable SSE stream (ADR-0006 rule 5)."""
    # LEFT JOIN: a build whose attempt has no chunks yet still has a row, with an empty log. No row: no build.
    log = await fetch_one(
        conn,
        LogText,
        """
        SELECT coalesce(string_agg(c.content, '' ORDER BY c.seq), '') AS text
        FROM job j LEFT JOIN log_chunk c ON c.job_id = j.id AND c.attempt = %(attempt)s
        WHERE j.build_id = %(build_id)s
        GROUP BY j.id
        """,
        {"build_id": build_id, "attempt": attempt},
    )
    if log is None:
        raise HTTPException(404, "build not found")
    return log.text


async def _organization_id(conn: AsyncConnection, slug: str) -> int:
    """The id of the organization with this slug. 404 if there is none."""
    row = await fetch_one(conn, Id, "SELECT id FROM organization WHERE slug = %(slug)s", {"slug": slug})
    if row is None:
        raise HTTPException(404, "organization not found")
    return row.id


@contextmanager
def _constraint_errors() -> Generator[None, None, None]:
    """Turn the schema's verdict on user input into HTTP answers. The constraint name says what was wrong.

    A CHECK failure becomes 422, such as `project_slug_check`; a UNIQUE failure becomes 409. The formats then live
    in one place, the schema, instead of in the schema and in Python too.
    """
    try:
        yield
    except CheckViolation as error:
        detail = [{"type": "check_violation", "loc": ["body"], "msg": error.diag.constraint_name}]
        raise HTTPException(422, detail) from error
    except UniqueViolation as error:
        raise HTTPException(409, f"already exists ({error.diag.constraint_name})") from error
