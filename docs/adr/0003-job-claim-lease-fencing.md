# ADR-0003: Job claim, lease and fencing

Status: accepted 2026-11-05 (Gate B part 1).
The implementation is YOU BUILD IT (M1: runner claim loop; M2: state machine, lease and fencing). This ADR fixes the rules, not the code.

## Context

The control plane must accept only one live attempt per job, recover a crashed runner's job within 2 x lease TTL, and stop a zombie runner from overwriting a newer attempt (spec 5.2.2, 5.2.3, sections 9 and 17).

## Evidence

- `[SOURCE]` Runbot, 2020 to 2026: each builder claimed its own builds with an `UPDATE` over a sub-select using `FOR UPDATE ... SKIP LOCKED`. Commit 2821de79 replaced this with a central assigner because "200+ machine attacking the same database ... concurrent updates".
- `[SOURCE]` GitLab: optimistic locking (`lock_version`) on the `pending → running` transition. Losers move on to the next job; if all conflict, the runner gets `409` (`app/services/ci/register_job_service.rb`). The Redis lock there is only an optimisation.
- `[SOURCE]` GitLab fencing: a retry is a new job row with a new token; requests with the old token get `403` and the runner aborts (`retry_job_service.rb:37-60`, `lib/api/ci/helpers/runner.rb:95-131`).
- `[SOURCE]` GitLab lease: heartbeat written at most every 15 min; reaper after 30 min of silence (`build.rb:948-950`, `stuck_builds/drop_running_service.rb:8`).
- `[DOCS]` PostgreSQL: `SKIP LOCKED` "can be used to avoid lock contention with multiple consumers accessing a queue-like table" (https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE). `now()` is the start time of the current transaction (https://www.postgresql.org/docs/current/functions-datetime.html).

## Options: claim

1. Self-claim in one statement, with row locks that skip rows other claimers hold (`SKIP LOCKED`).
2. Optimistic: read a candidate, update only if it is still queued (version check), retry on a miss.
3. A central assigner process writes the owner; runners poll for their assignments.

## Options: fencing

- A. One job row per build. An `attempt` counter is incremented at each claim, and each attempt gets its own job token. Every job request carries job id, attempt and token.
- B. A new job row per attempt (GitLab style).

## Decision

Claim option 1, fencing option A.

## Why

- Option 1 never wastes a round trip on conflicts and needs no extra process. Runbot ran it for six years at 200+ hosts before outgrowing it; we will have 5 or fewer.
- Option 2 makes every idle runner race for the same head-of-queue row: correct, but conflicts grow with the number of runners.
- Option 3 gives better placement (cache affinity) but adds a single-instance process to supervise. It is the upgrade path if placement starts to matter.
- Option A keeps the spec's model (attempt number visible in every log line) and makes "no two active jobs per build" a plain unique constraint on the job's build. Attempt history goes into job events. Option B also works; it trades the counter for more rows.

## Rules (the invariants M2 tests check)

1. Claim, heartbeat, reap and complete are each one short transaction. Lease times come only from database time (`now()`). Runners never send timestamps.
2. A claim sets: state running, runner, attempt + 1, lease expiry, a new job token (stored hashed), claim time. It only picks jobs whose labels the runner satisfies, ordered by priority then age, and only if the runner is under its declared capacity (the server re-checks this).
3. Every job request must match job id, current attempt, token, and state running. Anything else gets `409` (fenced: the runner must stop and kill its containers). A bad token gets `401`.
4. A heartbeat moves the lease to `now() + TTL` and returns the cancel flag.
5. The reaper (Worker, every 5 s) handles running jobs whose lease expired: back to queued if `attempt < max_attempts`, else failed with reason `lease_expired_max_attempts` (poison job). If cancel was requested: cancelled.
6. Complete is idempotent: same attempt and same result returns `200` again; a different result returns `409`.
7. Self-fencing: if the runner has had no successful heartbeat for 2/3 of the TTL, measured from send time on its own monotonic clock, it kills the build and drops the job without waiting for the server. Because 2/3 TTL is less than TTL, a runner that isn't frozen stops before anyone else can claim the job.
8. Release (graceful shutdown): running goes straight back to queued. The attempt stays counted, so max attempts still bounds a job that keeps causing shutdowns.

## Starting parameters

- TTL 30 s, heartbeat 10 s, reaper 5 s, claim poll 1 s, max attempts 3.
- Worst-case recovery after `kill -9`: 30 s lease + 5 s reaper + 1 s poll = about 36 s, under the 60 s target (2 x TTL).
- Load: 20 concurrent builds = 2 heartbeat writes per second.

## What this cannot prevent

- Side effects a zombie already caused outside the control plane: registry pushes, network calls made by tests, CPU burned. Mitigations: tag images per attempt (`build-<id>-a<attempt>`); record a digest only from an accepted complete; only the Worker posts commit statuses and only the agent deploys, both from accepted results.
- A frozen runner (GC pause, `SIGSTOP`, suspended laptop) that wakes after its lease expired can run for a few seconds before its next request gets `409`. Self-fencing shortens that window but can't close it, because a frozen process can't check its clock.

## Consequences

- Automatic retries are for infrastructure failures only (lease expiry). A failing build command is a result, not a reason to retry.
- A user "rebuild" creates a new Build and a new Job; the old ones stay as history.
