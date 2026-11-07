"""Node API for runners and agents, the M1 part (contracts/v1.yaml).

A runner meets three secrets, each a bearer token in the Authorization header:
1. a registration token (from POST /api/orgs/{org}/runner-tokens) registers it once and returns its credential;
2. the credential authenticates each claim, and each claim returns a new job token;
3. the job token authenticates that attempt's job calls: logs, then complete.

M1 is the happy path, allowed to be bad (spec M1). Leases are set but never enforced, claims take no SKIP LOCKED,
and logs have no gap or size rules: M2 and M4 replace these parts (YOU BUILD IT, docs/stubs.md).
"""

import os
from dataclasses import dataclass, field
from typing import Annotated, Literal

from fastapi import APIRouter, Depends, HTTPException, Response
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from psycopg.errors import CheckViolation
from pydantic import BaseModel, Field, model_validator

from cloudforge.credentials import digest, new_secret
from cloudforge.db import Connection, fetch_one

router = APIRouter(prefix="/v1")

LEASE_TTL_S = 30  # a claim holds its job this long, unless a heartbeat renews it (ADR-0003, enforced from M2)
HEARTBEAT_INTERVAL_S = 10  # how often the runner renews the lease (ADR-0003, from M2)
LOG_LIMIT_BYTES = 10 * 1024 * 1024  # ADR-0006 rule 4
GITEA_URL = os.environ["CF_GITEA_URL"]  # as runners reach it
REGISTRY = os.environ["CF_REGISTRY"]  # as buildkitd pushes to it (docs/operations/local-setup.md)

# Reads `Authorization: Bearer <secret>`. auto_error=False: a missing header gives None, and _secret answers 401.
_bearer = HTTPBearer(auto_error=False)


def _secret(credentials: Annotated[HTTPAuthorizationCredentials | None, Depends(_bearer)]) -> str:
    """The request's bearer secret: a registration token, a runner credential or a job token. 401 if missing."""
    if credentials is None:
        raise HTTPException(401, "missing bearer token")
    return credentials.credentials


# A route parameter `token: Secret` receives the bearer secret (dependency injection, like db.Connection).
Secret = Annotated[str, Depends(_secret)]


class RunnerRegistration(BaseModel):
    """Body of POST /v1/runners/register: the runner's name, its labels, and how many jobs it runs at once."""

    name: str = Field(min_length=1, max_length=63)
    labels: list[Annotated[str, Field(pattern=r"^[a-z0-9][a-z0-9_.-]{0,62}$")]] = []
    capacity: int = Field(ge=1, le=32)


@dataclass
class Credential:
    """A new runner's id and credential. The credential authenticates its claims; the database keeps its hash."""

    id: int
    credential: str  # shown once


@dataclass
class Runner:
    """The runner row behind a credential: what it may claim, and how many jobs at once."""

    id: int
    organization_id: int
    pool: Literal["trusted", "untrusted"]
    state: str
    labels: list[str]
    capacity: int


@dataclass
class Source:
    """What to build: an exact commit, where to fetch it, and the app's folder in it."""

    clone_url: str
    commit_sha: str
    clone_token: str | None
    root_dir: str


@dataclass
class ImageTarget:
    """Where to push the image, and the most bytes it may have."""

    ref: str
    max_bytes: int


@dataclass
class Claim:
    """The answer to a claim: the job, this attempt's token, and its limits and timings (contracts/v1.yaml)."""

    job_id: int
    attempt: int
    job_token: str
    timeout_s: int
    source: Source
    image: ImageTarget
    secrets: list[dict[str, str]] = field(default_factory=list)  # build variables arrive in M4 (ADR-0007)
    lease_ttl_s: int = LEASE_TTL_S
    heartbeat_interval_s: int = HEARTBEAT_INTERVAL_S
    log_limit_bytes: int = LOG_LIMIT_BYTES


@dataclass
class ClaimedJob:
    """The row the claim query returns. claim_job turns it into a Claim."""

    job_id: int
    attempt: int
    build_id: int
    commit_sha: str
    root_dir: str
    timeout_s: int
    max_image_bytes: int
    organization: str
    project: str
    repository: str


@router.post("/runners/register", status_code=201)
async def register_runner(body: RunnerRegistration, token: Secret, conn: Connection) -> Credential:
    """Register a runner with a one-time registration token, and return its credential, shown once (R1).

    The token is spent in the same statement, and it decides the organization and the pool (ADR-0009): a runner
    can't choose them. 401 for an unknown, used or expired token.
    """
    credential, credential_hash = new_secret()
    # If the UPDATE finds no usable token, `spent` is empty and nothing is inserted. Two registrations with one
    # token can't both succeed: the second UPDATE waits for the first one's row lock, then sees used_at set.
    cursor = await conn.execute(
        """
        WITH spent AS (
            UPDATE registration_token SET used_at = now()
            WHERE token_hash = %(token_hash)s AND kind = 'runner' AND used_at IS NULL AND expires_at > now()
            RETURNING organization_id, pool
        )
        INSERT INTO runner (organization_id, pool, name, state, labels, capacity, credential_hash)
        SELECT organization_id, pool, %(name)s, 'active', %(labels)s, %(capacity)s, %(credential_hash)s FROM spent
        RETURNING id
        """,
        body.model_dump() | {"token_hash": digest(token), "credential_hash": credential_hash},
    )
    registered = await cursor.fetchone()
    if registered is None:
        raise HTTPException(401, "unknown, used or expired registration token")
    return Credential(registered[0], credential)


async def _locked_runner(conn: Connection, credential: str) -> Runner:
    """The runner behind this credential, locked until the transaction ends. 401 if unknown or revoked.

    FOR NO KEY UPDATE is the lock an UPDATE of the row would take. It makes another claim, pause or revoke of
    this runner wait for our commit. Foreign-key checks of rows that point to the runner don't wait.
    """
    runner = await fetch_one(
        conn,
        Runner,
        """
        SELECT id, organization_id, pool, state, labels, capacity FROM runner WHERE credential_hash = %(hash)s
        FOR NO KEY UPDATE
        """,
        {"hash": digest(credential)},
    )
    if runner is None or runner.state == "revoked":
        raise HTTPException(401, "unknown or revoked runner credential")
    return runner


@router.post("/jobs/claim", response_model=Claim, responses={204: {"description": "Nothing to run"}})
async def claim_job(credential: Secret, conn: Connection) -> Claim | Response:
    """J2, and B2 on the first attempt: the oldest highest-priority job this runner may take (ADR-0003 rule 2).

    One transaction from the credential check to the commit. The runner's row stays locked all along, so a pause
    or revoke can't slip in between, and two claims of one runner never overlap (its capacity holds).
    204 when there is nothing to run, 401 for an unknown or revoked credential.
    """
    job_token, token_hash = new_secret()
    async with conn.transaction():
        runner = await _locked_runner(conn, credential)
        if runner.state == "paused":
            return Response(status_code=204)
        # `picked` chooses the job: queued, of the runner's organization and pool, needing only labels the runner
        # has, while the runner has a free slot. The UPDATE then takes it: running, attempt + 1, the new token's
        # hash, the lease and the deadline. It checks `j.state = 'queued'` again: if another runner's claim took
        # the job first, this one updates nothing and answers 204 (M2 adds SKIP LOCKED, docs/stubs.md).
        job = await fetch_one(
            conn,
            ClaimedJob,
            """
            WITH picked AS (
                SELECT j.id FROM job j JOIN build b ON b.id = j.build_id JOIN project p ON p.id = b.project_id
                WHERE j.state = 'queued'
                  AND p.organization_id = %(organization_id)s
                  AND b.trusted = %(trusted)s  -- fork builds only on the untrusted pool, and only they (ADR-0009)
                  AND j.labels <@ %(labels)s::text[]
                  AND (SELECT count(*) FROM job WHERE runner_id = %(runner_id)s AND state = 'running') < %(capacity)s
                ORDER BY j.priority DESC, j.id
                LIMIT 1
            )
            UPDATE job j
            SET state = 'running', attempt = j.attempt + 1, runner_id = %(runner_id)s, token_hash = %(token_hash)s,
                lease_expires_at = now() + make_interval(secs => %(lease_ttl_s)s),
                deadline_at = now() + make_interval(secs => p.build_timeout_s), claimed_at = now()
            FROM picked, build b, project p, organization o, git_repository r
            WHERE j.id = picked.id AND j.state = 'queued'
              AND b.id = j.build_id AND p.id = b.project_id AND o.id = p.organization_id AND r.id = b.repository_id
            RETURNING j.id AS job_id, j.attempt, b.id AS build_id, b.commit_sha, p.root_dir,
                      p.build_timeout_s AS timeout_s, p.max_image_bytes, o.slug AS organization,
                      p.slug AS project, r.full_name AS repository
            """,
            {
                "organization_id": runner.organization_id,
                "trusted": runner.pool == "trusted",
                "labels": runner.labels,
                "runner_id": runner.id,
                "capacity": runner.capacity,
                "token_hash": token_hash,
                "lease_ttl_s": LEASE_TTL_S,
            },
        )
        if job is None:
            return Response(status_code=204)
        # B2: the first attempt starts the build. A later attempt finds it running already and changes nothing.
        await conn.execute(
            "UPDATE build SET state = 'running', started_at = now() WHERE id = %(id)s AND state = 'queued'",
            {"id": job.build_id},
        )
    return Claim(
        job_id=job.job_id,
        attempt=job.attempt,
        job_token=job_token,
        timeout_s=job.timeout_s,
        source=Source(f"{GITEA_URL}/{job.repository}.git", job.commit_sha, None, job.root_dir),  # public repos in M1
        # A tag per attempt (ADR-0008). Deployments use the image's digest, never this tag.
        image=ImageTarget(
            f"{REGISTRY}/{job.organization}/{job.project}:{job.commit_sha[:12]}-b{job.build_id}-a{job.attempt}",
            job.max_image_bytes,
        ),
    )


class LogChunk(BaseModel):
    """Body of POST .../logs: one piece of the attempt's log. `seq` numbers the pieces from 0."""

    seq: int = Field(ge=0)
    text: str = Field(pattern=r"^[^\x00]*$")  # PostgreSQL text can't hold NUL; the runner replaces it (ADR-0006)


class Completion(BaseModel):
    """Body of POST .../complete: how the attempt ended."""

    result: Literal["succeeded", "failed", "cancelled"]
    image_digest: str | None = Field(default=None, pattern=r"^sha256:[0-9a-f]{64}$")
    reason: str | None = None  # step_failed:<step>, timeout or image_too_large

    @model_validator(mode="after")
    def _required_by_result(self):
        """Rules across fields, checked after the field checks: success needs a digest, failure a reason."""
        if self.result == "succeeded" and self.image_digest is None:
            raise ValueError("a succeeded build needs its image_digest")
        if self.result == "failed" and self.reason is None:
            raise ValueError("a failed build needs a reason")
        return self


@dataclass
class Job:
    """The job a job token belongs to. Nothing sets the cancel flag yet."""

    id: int
    attempt: int
    state: str
    build_id: int
    cancel_requested: bool


@dataclass
class JobAck:
    """The answer to a log chunk: whether someone asked to cancel the job, so the runner can stop it."""

    cancel_requested: bool


async def _job(job_id: int, attempt: int, token: Secret, conn: Connection) -> Job:
    """The job this token was issued for, checked against the attempt in the URL (ADR-0003 rule 3).

    401 when the token isn't the job's current token. Each claim replaces the token, so an older attempt's token
    gets 401 too, where ADR-0003 rule 3 asks for 409 (an open question for M2, docs/progress.md).
    409 when the token is current but the URL names another attempt.
    """
    job = await fetch_one(
        conn,
        Job,
        """
        SELECT id, attempt, state, build_id, cancel_requested_at IS NOT NULL AS cancel_requested
        FROM job WHERE id = %(id)s AND token_hash = %(token_hash)s
        """,
        {"id": job_id, "token_hash": digest(token)},
    )
    if job is None:
        raise HTTPException(401, "unknown job token")
    if job.attempt != attempt:
        raise HTTPException(409, "fenced: a newer attempt owns this job")
    return job


def _require_running(job: Job) -> None:
    """409 unless the attempt still runs: an attempt that is over can't send logs or another result."""
    if job.state != "running":
        raise HTTPException(409, "fenced: this attempt is over")


@router.post("/jobs/{job_id}/attempts/{attempt}/logs", status_code=202)
async def append_log(body: LogChunk, job: Annotated[Job, Depends(_job)], conn: Connection) -> JobAck:
    """Store one log chunk of a running attempt, and answer whether the job should be cancelled.

    Each `seq` is stored once: a resent chunk is ignored, so a runner may resend after a lost answer.
    409 if the attempt is over, 413 for a chunk over 64 KiB.
    Gaps, limits and resume are M4's (YOU BUILD IT, ADR-0006).
    """
    _require_running(job)
    try:
        await conn.execute(
            "INSERT INTO log_chunk (job_id, attempt, seq, content) VALUES (%(job_id)s, %(attempt)s, %(seq)s, %(text)s)"
            " ON CONFLICT DO NOTHING",
            {"job_id": job.id, "attempt": job.attempt, "seq": body.seq, "text": body.text},
        )
    except CheckViolation as error:  # log_chunk holds at most 64 KiB
        raise HTTPException(413, "the chunk is over 64 KiB") from error
    return JobAck(job.cancel_requested)


@router.post("/jobs/{job_id}/attempts/{attempt}/complete")
async def complete_job(body: Completion, job: Annotated[Job, Depends(_job)], conn: Connection) -> Response:
    """End the attempt and its build: succeeded, failed or cancelled (J3, J4 or J5, with B3, B4 or B5).

    The same result again is accepted again (ADR-0003 rule 6), so a runner can retry when an answer got lost.
    409 for a different result once the attempt is over, or for a cancel that nobody asked for.
    """
    if job.state == body.result:  # a retry of a completion that went through
        return Response()
    _require_running(job)
    if body.result == "cancelled" and not job.cancel_requested:
        raise HTTPException(409, "nobody asked to cancel this job")
    params = body.model_dump() | {"id": job.id, "build_id": job.build_id}
    async with conn.transaction():
        cursor = await conn.execute(
            "UPDATE job SET state = %(result)s, finished_at = now() WHERE id = %(id)s AND state = 'running'", params
        )
        if cursor.rowcount == 0:  # someone moved first: the job changed after _job read it
            raise HTTPException(409, "fenced: this attempt is over")
        await conn.execute(
            """
            UPDATE build SET state = %(result)s, image_digest = %(image_digest)s, reason = %(reason)s, finished_at = now()
            WHERE id = %(build_id)s AND state = 'running'
            """,
            params,
        )
    return Response()
