# Failure scenarios and targets

Status: accepted 2026-11-06 (Gate B part 2 of 3).
Timings come from ADR-0003: lease 30 s, heartbeat 10 s, self-fence 20 s, reaper 5 s, claim poll 1 s, max attempts 3. Transition ids (J6, B4, ...) refer to `state-machines.md`.

## 1. The 10 failure scenarios (spec section 9)

All ten run in `make chaos` (M2). Two kinds of test client are used `[OUR DECISION]`:

- **The real runner**, for crashes and network loss.
- **A fake runner**: a small script that speaks the runner protocol and misbehaves on purpose (claims and vanishes, completes twice, replays an old attempt). This makes faults exact that are hard to time with real processes.

Every scenario must also pass three common checks:
- Two attempts of one job never hold a valid lease at the same moment.
- The job ends in exactly one terminal state.
- The event history explains every step.

| # | Scenario | What the system does | Pass condition | How the test causes it |
|---|---|---|---|---|
| 1 | Runner crashes mid-build (`kill -9`) | Heartbeats stop. The lease expires at most 30 s after the last one, the reaper requeues within 5 s (J6), and the next poll claims attempt 2. | Second claim ≤ 60 s after the kill. Build succeeds on attempt 2. Events show `lease_expired`. | Real runner (two running). `docker kill -s KILL` while a slow fixture builds. |
| 2 | Runner loses network and comes back after its lease expired | The runner self-fences at 20 s and kills its build. The reaper requeues at about 35 s and another runner takes attempt 2. When the network returns, the old runner's queued calls for attempt 1 get 409 and it drops them. | Every late call from attempt 1 gets 409. No attempt-1 log chunk is accepted after the requeue. Attempt 2 succeeds. | Real runner. `docker network disconnect` for 45 s. |
| 3 | Control plane restarts mid-job | Runner calls fail; it retries with backoff and keeps log chunks in memory. A restart under 20 s costs nothing: the lease (database time) is still valid and the next heartbeat extends it. A Worker restart only pauses the reaper and webhook processing; both resume from the database. | The same attempt finishes. No gap in the log sequence. No requeue. | Real runner. `docker compose restart api worker`. |
| 4 | Redis unavailable | Profile A has no Redis, so this becomes **PostgreSQL down for 60 s** `[OUR DECISION]`. The API answers 503. Runners self-fence after 20 s. When the database returns, the leases have expired and the reaper requeues every running job. | No double execution. Every job finishes on a later attempt or fails cleanly. The API never hangs. | `docker compose stop postgres`, wait 60 s, start it. |
| 5 | Job assigned but never started | Our claim is the start: it sets `running` and a lease in one transaction. If the claim response is lost, or the runner dies before starting, nobody heartbeats. The lease expires and the reaper requeues (J6). The lost attempt still counts. | Claimed again within 36 s, as attempt 2. | Fake runner claims, then exits. |
| 6 | Runner reports the same result twice | Complete is idempotent (ADR-0003 rule 6): an identical repeat gets 200 and writes nothing. The deployment is created once, because J3 happened once. | 200 twice, one `succeeded` event, one deployment. Same result when the two completes run in parallel. | Fake runner, sequential and parallel. |
| 7 | Runner reports a result for a stale attempt | Attempt and token don't match the job's current attempt: 409 (ADR-0003 rule 3). | 409. Job state and digest unchanged. Attempt 2's complete then succeeds. | Fake runner A claims; the test forces its lease to expire; fake runner B claims; A completes. |
| 8 | Two runners race to claim the same job | The claim locks the job row with `SKIP LOCKED`. A competing claim skips that row and takes another job, or gets 204. | Exactly one 200 per job and attempt. One claim event per attempt. | 20 fake runners poll 5 queued jobs at once, repeated 100 times. |
| 9 | Developer cancels a running build | The API sets the cancel flag. The runner sees it in its next response (about 1 s while logs flow, at most 10 s otherwise), cancels the BuildKit solve and reports cancelled (J5). If the runner is gone, the reaper cancels at lease expiry (J8). | Build cancelled ≤ 15 s after the request. No chunk accepted after the terminal state. | Real runner. Cancel through the API mid-build. |
| 10 | Poison job fails repeatedly | A job that kills its runner every time ends each attempt by lease expiry. Example: a build whose output makes a naive runner run out of memory. After attempt 3 it fails with `lease_expired_max_attempts` (J7). An ordinary failing build is not poison: it fails on attempt 1 and is never retried. | Exactly 3 claims, final state `failed`, no 4th claim. | Fake runner that dies on any job carrying a test label. |

What fencing cannot stop (side effects of a zombie, the frozen-process window) is in ADR-0003. The M2 "break it" session reproduces one on purpose.

Not verified: whether buildkitd cancels a solve when the runner that started it dies. If it doesn't, a crashed runner's build may still finish and push. The per-attempt image tag (ADR-0003) makes that harmless. The M1 "break it" session checks the real behavior.

## 2. Targets (spec section 17)

| Target | Spec | Ours | Why | Measured by | Milestone |
|---|---|---|---|---|---|
| Webhook acknowledged | < 500 ms p95 | same | the intake is one INSERT (ADR-0004) | load script: 200 signed deliveries, p95 response time | M3 |
| Idle runner claims a new job | < 2 s | same | poll every 1 s ± 20 % jitter | `claimed_at − created_at` with an idle runner, over 50 jobs | M2 |
| Crashed runner's job recovered | < 2 × TTL = 60 s | same; expect about 36 s | 30 s lease + 5 s reaper + 1 s poll | chaos scenario 1 | M2 |
| Failed requests during a redeploy under load | 0 | same | blue-green (ADR-0005) and Nginx's graceful reload | load generator at 50 req/s through a redeploy: zero non-2xx, zero connection errors | M6 |
| 20 concurrent builds on one node, no double execution | as stated | **changed:** 20 concurrent light jobs; real app builds capped at 3 at a time on this host | this host has about 5 GB of free RAM; 20 real Next.js builds need much more `[INFERRED]` | 4 runner instances × 5 slots. The fixture sleeps 30 s and takes a per-job cache-busting argument, so BuildKit can't merge identical builds. Check that no job ever has two overlapping leases. | M2 |

Added targets `[OUR DECISION]`:

| Target | Value | Measured by | Milestone |
|---|---|---|---|
| Push → build queued | < 5 s p95 | `build.created_at − delivery.received_at` | M3 |
| Cancel → build stopped | < 15 s | chaos scenario 9 | M2 |
| Log line → visible in the browser | < 3 s p95 | a fixture prints timestamps; the test reads the SSE stream | M4 |
| Build succeeded → deployment live | < 60 s for the Express fixture | `deployment live time − build finish time` | M6 |
| Dead runner shown as offline | < 30 s | API runner status after `kill -9` | M4 |
