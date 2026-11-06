# Gate A: Research findings

Date: 2026-11-05. Status: awaiting approval.

## Evidence labels

- `[SOURCE]`: read in source code during this session. Cited as `repo path:line` at the snapshot below.
- `[DOCS]`: read in official documentation during this session (link given).
- `[INFERRED]`: my conclusion, not stated in code or docs.
- `[OUR DECISION]`: CloudForge design choice (none final yet; all go to Gate B).
- `[RAN]`: command executed on your machine during this session.

## Source snapshots (read only, nothing copied)

| Repo | Commit | Date | License |
|---|---|---|---|
| github.com/odoo/runbot | 67892b2 (full history fetched) | 2026-11-04 | LGPL-3 (`runbot/__manifest__.py:71`) |
| gitlab.com/gitlab-org/gitlab-runner | 0d29aea | 2026-11-05 | MIT (`LICENSE`) |
| gitlab.com/gitlab-org/gitlab (sparse: CI services/models, runner API, Workhorse) | 0835e894 | 2026-11-05 | MIT Expat outside `ee/` and `jh/` (`LICENSE`). All files read are outside `ee/`. |
| github.com/moby/buildkit (sparse: README, docs, buildctl, progressui) | b9d02e6 | 2026-11-04 | Apache-2.0 |

---

## 1. Odoo Runbot

### 1.1 Shape of the system

- `[SOURCE]` There is no runner API. Leader, builder and assigner are Odoo scripts that open the Runbot PostgreSQL database directly with `--db_host/--db_user/--db_password` (`runbot_builder/tools.py:158-210`). The trust boundary is the database credential: every build host can read and write the whole Runbot database.
- `[SOURCE]` One `runbot.host` row per machine. Roles are boolean flags: `is_leader`, `is_builder`, `is_assigner`, `is_registry`, `is_backup` (`runbot/models/host.py:54-59`). Liveness is visible through `last_start_loop`, `last_end_loop`, `last_success` (`host.py:28-30`, written each loop in `runbot_builder/tools.py:77-87`).

### 1.2 Terms verified in code

| Term | What it is in the code |
|---|---|
| bundle | Branches with the same name across the repos of a project, looked up by `name` + `project_id` (`runbot/models/branch.py:195`). `is_base` bundles (e.g. `master`, `18.0`) are `sticky` (`bundle.py:31-36, 101-103`). |
| batch | One snapshot of commits for a bundle. States `preparing / ready / done / skipped` (`batch.py:24`). |
| trigger | Project + triggering repos + dependency repos + config. Also `ci_context` (commit status name), `on_base/on_dev/on_staging` (branch-type policy), `network_enabled`, `use_extra_slot` (`runbot/models/repo.py:61-93`). |
| config / step | Ordered steps; types `install_odoo, run_odoo, python, create_build, configure_upgrade, test_upgrade, restore, upgrade_cluster, dynamic, semgrep` (`build_config.py:418-429`). Per-step `cpu_limit` default 3600 s (`build_config.py:458`). One container per step. |
| host | Build node (`runbot.host`). |
| per-build database | `createdb <db> -T <template>` then `psql < dump.sql` from a dump zip produced by another build (`build_config.py:1270-1290, 1308-1318`). The template is the config parameter `runbot.runbot_db_template` (default `template0`), created per host at bootstrap (`host.py:140-150`). |

### 1.3 How builds are triggered

1. `[SOURCE]` GitHub calls `/runbot/hook/<remote_id>`. `verify_signature` checks `X-Hub-Signature-256` (HMAC-SHA256, constant-time compare) only if a secret is configured; the code says `TODO enforce webhook_secret` (`runbot/controllers/hook.py:18-35`).
2. `[SOURCE]` On `push`, or `pull_request` with action `synchronize/opened/reopened`, the hook only records `hook_time` (`hook.py:66-72`). It does not read commits from the payload. The webhook is a doorbell.
3. `[SOURCE]` The leader loop fetches a repo when `hook_time > last_processed_hook_time` (mode `hook`) or every 5 minutes (mode `poll`, the default) (`repo.py:435-442, 754-777`), then diffs refs to find new commits (`repo.py:709-719`). Git is the source of truth, so a lost or duplicated webhook is harmless.
4. `[SOURCE]` A new head on a branch is added to the bundle's batch in state `preparing`. A newer push during the throttle window replaces the commit ("New head on branch ... during throttle phase") (`batch.py:86-104`, `repo.py:696-707`). After `process_delay` the batch is prepared and builds are created per trigger (`batch.py:129-146`). This is push coalescing (debounce).
5. `[SOURCE]` Dedup by content: `runbot.build.params` stores a sha256 `fingerprint` of the build parameters with a unique constraint; `create()` returns the existing record (`build.py:133-192, 268-282`). `_create_build` reuses an existing build with the same params unless it was killed or skipped (`batch.py:148-189`). Two bundles with identical commits and config share one build.

### 1.4 How jobs are assigned

- `[SOURCE]` Today: a central assigner. `_assign_pending_builds()` runs on the host flagged `is_assigner` every 5 s (`runbot_builder/assigner.py`; the leader can also run it, `leader.py`). It counts testing + pending builds per host, sorts hosts by free slots and assigns in three passes: priority builds may use `nb_worker + 1` slots, normal `nb_worker`, scheduled (nightly) `nb_worker - 1`. It commits, then re-checks `not build.host and local_state == 'pending'` before writing `host` (`runbot.py:81-133`).
- `[SOURCE]` History: from 2020 ("runbot 5.0", commit 45721cdf) each builder claimed its own builds with `UPDATE runbot_build SET host = ... WHERE id IN (SELECT ... FOR UPDATE OF runbot_build SKIP LOCKED LIMIT n)`. Commit 2821de79 (2026-07-20, "make build assignment centralized") removed it. Its message gives the reasons: "200+ machine attacking the same database to assign build can lead to performance issues, concurrent updates", plus better placement (same commit on the same host) and fewer reserved slots.
- `[SOURCE]` The builder then starts only builds assigned to it, limited by free slots, +1 for priority (`runbot.py:136-145`).

### 1.5 How a builder executes (a reconciler)

- `[SOURCE]` `_scheduler(host)` runs one loop turn: process kill and wake-up requests, process host messages, ingest logs, check OOM kills, call `_schedule()` for each testing or running build, update global states, garbage-collect running builds, regenerate nginx (`runbot.py:46-79`).
- `[SOURCE]` `_schedule()` (`build.py:1075-1190`): pending → initialize; testing → check the container. If RUNNING past `min(step cpu_limit, runbot_timeout)` → kill. If UNKNOWN or GHOST for over 60 s → treat as a failed start. When a step has ended → compute results, start the next step, or stop early on failure (`break_after_if_ko`).
- `[SOURCE]` Step state lives outside the builder process. The container entrypoint writes `signals/start-<name>` and `signals/end-<name>` files into the bind-mounted build directory. `docker_state()` combines these files with `docker inspect` into `VOID / UNKNOWN / RUNNING / GHOST / END` (`runbot/container.py:270-285, 387-415`).
- `[INFERRED]` A builder restart is safe because each loop rebuilds its view from the database, the files and Docker. This is the same idea as our deploy agent's reconcile loop.
- `[SOURCE]` Container flags in `_docker_run`: `network_mode='none'` unless the trigger enables network, non-root `user`, `mem_limit`, CPU quota, `RLIMIT_CPU` ulimit = step CPU seconds, core dumps disabled, `init=True`, `auto_remove`, ports published on `127.0.0.1` only (`container.py:320-347`). Not set there: capability drop, pids limit, read-only root filesystem.

### 1.6 Cancellation and superseded builds

- `[SOURCE]` `_ask_kill`: pending descendants are set to `done/killed` immediately in the database. For testing or running builds, it inserts a `runbot.host.message('kill')` row for the owning host (`build.py:1461-1484`). That host's loop processes the message and calls `_kill()`, which does nothing unless `build.host` is the current host, then stops the container and marks the build `done` (`build.py:1447-1459`). Cancellation is pull-based.
- `[SOURCE]` When a new batch is prepared for a non-sticky bundle, older unfinished batches are skipped (`batch.py:453-464`). `_skip()` marks pending builds `skipped`. Testing builds are only flagged `killable` (`batch.py:106-127`). `_gc_testing` kills killable builds only when the host is out of slots and other work is pending (`runbot.py:161-196`). A build shared through dedup is left alone while another bundle's latest batch, or any base bundle, still needs it.

### 1.7 Failure recovery

- `[SOURCE]` There is no lease. A build is bound to `host` and to its build directory on that host's disk.
- `[SOURCE]` Loop errors: `_manage_host_exception` rolls back, stores `last_exception` and `exception_count`, sleeps a random 0-3 s (`runbot.py:287-308`).
- `[SOURCE]` Timeouts: per-step CPU limit and global `runbot.runbot_timeout`; 60 s container-start timeout (`build.py:1075-1110`).
- `[INFERRED]` I found no code that moves a dead host's `testing` builds to another host (searched `runbot.py`, `host.py`, `build.py`). This is absence of evidence, not proof.

### 1.8 Logs, routing, previews

- `[SOURCE]` Logs: the container writes JSON lines to a file. The host reads from a byte offset persisted in `odoo_log.seek` and inserts rows into the central database (`host.py:295-330`). This is resume-from-offset.
- `[SOURCE]` Routing: each builder renders an nginx config from its `running` builds, writes it only if it changed, then sends `SIGHUP` to nginx (or starts nginx) (`runbot.py:198-235`). One `server` block per running build with a regex `server_name` containing the build name and an access token, then `proxy_pass http://127.0.0.1:<port>` (`runbot/templates/nginx.xml:83-105`). Host ports are allocated by scanning used ports from 2000 (`build.py:982-990`).
- `[SOURCE]` Running builds are Runbot's previews. Each host keeps the `running_max` most recent ones and kills older ones, except sticky bundles and builds protected by `gc_running_date` (`runbot.py:147-159`).
- `[SOURCE]` Commit status: each trigger has a `ci_context`; statuses are prepared per build (`batch.py:189`) and sent by the leader when `send_status` is set (`leader.py:31`).

---

## 2. GitLab Runner and the GitLab server

### 2.1 Runner authentication

- `[SOURCE]` Two credential types. (a) Runner authentication token, created in the UI or API with prefix `glrt-`, verified with `POST /api/v4/runners/verify` together with a `system_id` that identifies the machine (`gitlab lib/api/ci/runner.rb:129-146`, `app/models/ci/runner.rb:59-62`). (b) Legacy registration token: `POST /api/v4/runners` exchanges it for a runner token, and can answer `410 Gone` when registration tokens are disabled (`runner.rb:19-70`). A token reset endpoint exists (`gitlab-runner network/gitlab.go:562-650`).
- `[SOURCE]` Job token: each job response carries a token. The runner sends it as the `Job-Token` header on log uploads and in the body of status updates (`network/gitlab.go:735-760, 834-870`). The server answers `403` if the job is not `running` or `canceling` ("Job is not processing on runner") or if the token expired (`lib/api/ci/helpers/runner.rb:95-131, 279`).

### 2.2 Polling

- `[SOURCE]` Runner side: `feedRunners` puts each configured runner in a channel every `check_interval / number_of_runners` (default `CheckInterval = 3s`) (`commands/multi.go:1120-1137`, `common/consts.go:13`). Workers call `POST /api/v4/jobs/request` with the runner token, `system_id` and `last_update` (`network/gitlab.go:651-733`).
- `[SOURCE]` Response codes: `201` job, `204` no job, `403` bad token, `429/503` back off, other 4xx = client error (`gitlab.go:695-731`). After receiving a job, the runner polls again immediately unless `strict_check_interval` is set (`multi.go:1362-1366`). `request_concurrency` limits in-flight requests per runner (`multi.go:1262-1264`).
- `[SOURCE]` Server cheap path: each runner has a Redis key `runner:build_queue:<token>` holding a random value. If the runner's `last_update` equals it, the server returns `204` without scanning the queue (`lib/api/ci/helpers/job_request.rb:42-73`, `app/models/ci/runner.rb:526-545, 663-665`). The value changes (`tick_runner_queue`) when this runner's queue may have changed.
- `[SOURCE]` Long polling: Workhorse, a Go reverse proxy in front of Rails, intercepts `jobs/request`. If a polling duration is configured, it watches that Redis key: no change until timeout → `204`; changed before watching → proxy to Rails; changed while watching → `204` so the runner asks again. Duration `0` disables it (`workhorse/internal/builds/register.go:121-187`).

### 2.3 Assignment and double-claim prevention

`[SOURCE]` `Ci::RegisterJobService#process_queue` (`app/services/ci/register_job_service.rb:111-171, 217-290, 438-479`):

1. Read candidate jobs from the pending queue, from database replicas, up to a maximum depth.
2. Take a Redis lease `build/register/<id>` for 3 s (`TEMPORARY_LOCK_TIMEOUT`). If taken, skip to the next job. This only avoids wasted work; it is not the safety guarantee.
3. Re-read the job on the primary; skip if no longer `pending`.
4. `assign_runner!` runs the state transition `pending → running` and saves with optimistic locking (`lock_version`, see `app/models/commit_status.rb:113-117`). The loser gets `StaleObjectError` or `InvalidTransition` and tries the next job.
5. If every candidate conflicted, the API answers `409 Conflict` and the runner retries.

`[INFERRED]` So GitLab uses optimistic concurrency (version column + state machine), not `SKIP LOCKED`.

### 2.4 Logs, heartbeat, cancellation

- `[SOURCE]` Logs: `PATCH /jobs/:id/trace` with `Content-Range: start-end`. `202` = accepted. `416` = offset mismatch; the server returns `Range: 0-<size>` and the runner rewinds to that offset (`gitlab-runner network/gitlab.go:834-955`, `network/patch_response.go:21-29`; server `lib/api/ci/runner.rb:333-355`).
- `[SOURCE]` Heartbeat: if no log was sent for `forceSendInterval` (30 s to 30 min), the runner sends `PUT /jobs/:id` with `state=running` (`network/trace.go:372-395`, `common/consts.go:54-58`). The server only writes `updated_at` when it is older than 15 minutes (`app/services/ci/update_build_state_service.rb:96-99`, `app/models/ci/build.rb:948-950`). The server can stretch the runner's update interval with `X-GitLab-Trace-Update-Interval`.
- `[SOURCE]` Cancellation: both responses carry a `Job-Status` header. `canceling` → graceful cancel (the runner's cancel function lets cleanup run). `canceled`, `failed` or HTTP `403` → hard abort (`network/remote_job_state_response.go`, `network/trace.go:279-305`). The control plane never connects to the runner.
- `[SOURCE]` Final status update is retried with backoff. `202` = server still finalizing the log → wait and retry. `412` = log checksum mismatch → retry (`network/trace.go:224-260`).

### 2.5 Failure recovery

- `[SOURCE]` Reapers: running job with `updated_at` older than 30 min → dropped as `no_updates_running`; canceling for 30 min → `no_updates_canceling`; pending for 1 day → dropped; pending 1 h with no matching runner → dropped (`app/services/ci/stuck_builds/*.rb`). Separate server-side job timeouts: `app/services/ci/timed_out_builds/`.
- `[SOURCE]` A retry is a new job row: `Ci::RetryJobService#clone!` creates a new job through `Ci::CloneJobService` (`app/services/ci/retry_job_service.rb:37-60`).
- `[INFERRED]` That gives fencing for free: a new attempt has a new ID and a new job token. The old runner's token belongs to a job that is no longer running, so its next request gets `403` and it aborts. A stale writer can do nothing through the API.
- `[SOURCE]` Graceful shutdown: `SIGQUIT` = stop requesting jobs and wait for running ones; `SIGTERM`/`SIGINT` = forceful (`commands/multi.go:1700-1776`; `shutdown_timeout` default 30 s, `common/consts.go:22`).

---

## 3. BuildKit

- `[SOURCE]` A frontend (for example the Dockerfile frontend) converts the build definition into LLB, a DAG ("LLB is to Dockerfile what LLVM IR is to C"). Features: concurrent dependency resolution, instruction caching, cache import/export, automatic garbage collection (`buildkit README.md`).
- `[SOURCE]` Cache export: `inline` (stored with the image, `min` mode only) or `registry` (separate ref, `mode=min|max`; `max` exports intermediate stages too), plus `local`, `gha`, `s3` (`README.md`, "Export cache"). `[INFERRED]` A test stage that is not in the final image is only cached remotely with `registry` + `mode=max`.
- `[SOURCE]` Progress output modes: `auto, plain, tty, rawjson` (`cmd/buildctl/build.go:52`, `util/progress/progressui/display.go:138`). `rawjson` is the structured stream to evaluate in the per-step status ADR.
- `[DOCS]` The default `docker` buildx driver supports the inline, local, registry and gha cache backends "only if you have enabled the containerd image store" (https://docs.docker.com/build/cache/backends/).
- `[SOURCE]` Rootless BuildKit runs under RootlessKit. Inside a container it needs `seccomp=unconfined` and `apparmor=unconfined`, or `--privileged` ("almost safe for rootless") (`docs/rootless.md:13-90`).

## 4. Nginx dynamic routing and alternatives

- `[DOCS]` On `HUP` the master checks syntax and opens new sockets. On failure it rolls back and keeps the old config. On success it starts new workers; old workers close their listen sockets and keep serving existing clients until done (https://nginx.org/en/docs/control.html). `[INFERRED]` Reload is atomic per config, but long-lived connections (SSE, WebSocket) keep old workers alive.
- `[DOCS]` If `proxy_pass` contains variables, a host name is resolved at request time through `resolver`; otherwise at config load (https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass). Regex `server_name` supports named captures, e.g. `~^(www\.)?(?<domain>.+)$` (https://nginx.org/en/docs/http/server_names.html).
- `[DOCS]` User-defined bridge networks give containers automatic DNS resolution by name; the default bridge does not (https://docs.docker.com/engine/network/drivers/bridge/).
- `[DOCS]` Traefik's Docker provider reads containers from the Docker API; its example mounts `/var/run/docker.sock` (https://doc.traefik.io/traefik/reference/install-configuration/providers/docker/). Docker: "only trusted users should be allowed to control your Docker daemon", because a container can mount the host `/` (https://docs.docker.com/engine/security/).
- `[DOCS]` Caddy: `POST /load` replaces the config, "lightweight, efficient, and incur zero downtime" (https://caddyserver.com/docs/api). Local HTTPS uses Caddy's own root CA installed into the trust store (https://caddyserver.com/docs/automatic-https).
- `[SOURCE]` Runbot uses "render config, diff, SIGHUP" (section 1.8).

## 5. Vercel (public docs only)

- `[DOCS]` Automatic deployments on every branch push; a preview deployment for every push; a production deployment for the production branch (https://vercel.com/docs/git).
- `[DOCS]` Environments: Production, Preview, Development (custom ones on paid plans). Each has its own variables; branch-specific preview values override preview values (https://vercel.com/docs/deployments/environments).
- `[DOCS]` A pull request from a fork needs authorization from a team member before it deploys, unless the commit author is a team member: "protects you from leaking sensitive project information" (https://vercel.com/docs/git).
- `[DOCS]` Instant Rollback restores a previous production deployment; environment variable changes are not applied ("will roll back to a previous build"); Hobby can only go back one deployment (https://vercel.com/docs/instant-rollback). `[INFERRED]` Production is a pointer (alias) to an immutable deployment, so rollback is a pointer move.
- Not claimed: build isolation technology, scheduler, routing layer. Not in the docs I read.

## 6. Render (public docs only)

- `[DOCS]` Deploy sequence: build fails → deploy cancelled, old instance keeps serving. Build succeeds → new instance starts while the old one gets all traffic → when healthy, networking switches → 60 s later `SIGTERM` to the old instance → `SIGKILL` after the shutdown delay (default 30 s). A persistent disk disables zero-downtime deploys (https://render.com/docs/deploys).
- `[DOCS]` Health checks every few seconds; HTTP `2xx/3xx` within 5 s. A new deploy must become healthy within 15 min or it is cancelled. A running instance failing for 15 s stops receiving traffic; after 60 s it is restarted (https://render.com/docs/health-checks).
- `[DOCS]` PR previews: separate temporary instance with its own URL; it copies all settings from the base service, including environment variables "such as database connection information"; `IS_PULL_REQUEST` tells the app it is a preview; deleted when the PR is merged or closed (https://render.com/docs/service-previews). Note: by default a preview can reach the production database. CloudForge's spec forbids this.
- `[DOCS]` Rollback is a new deploy that reuses the target deploy's build artifact (no rebuild). Artifact retention depends on plan. Dashboard rollback disables auto-deploy; API rollback does not. Disks are not rolled back (https://render.com/docs/rollbacks).

## 7. Odoo.sh (for spec section 7)

- `[DOCS]` Staging branches use "neutralized duplicates of the production database" (scheduled actions disabled, etc.). Development branches "create new databases using demo data to run the unit tests" and can be garbage-collected (https://www.odoo.com/documentation/18.0/administration/odoo_sh/getting_started/branches.html).

## 8. Your machine

- `[RAN]` Ubuntu 22.04.5, kernel 6.8, bare metal, cgroup v2 (systemd driver), 8 CPUs, 15 GB RAM (10 GB in use at check time), 87 GB free on ext4.
- `[RAN]` Docker 29.9.2 rootful with default AppArmor and seccomp, `overlay2` storage driver (not the containerd image store), Compose v5.6.1, buildx v0.38.1 with the default `docker` driver (BuildKit v0.34.0). Your user is in the `docker` group, which is root-equivalent (see the Docker security docs above).
- `[RAN]` Go 1.26.3, Python 3.12.12, Node 20, psql 18.6 client. Not installed: nginx, mkcert, buildctl. All three can run as containers.
- `[RAN]` `getent hosts foo.cloudforge.localhost` → `::1`. `getent hosts foo.cloudforge.local` → nothing, because `/etc/nsswitch.conf` has `mdns4_minimal [NOTFOUND=return]` before `dns`: `.local` names never reach DNS. This is decision 5.2.9 shown on your own machine.
- `[DOCS]` + `[RAN]` `--storage-opt size` works with `overlay2` only on xfs mounted with `pquota` (https://docs.docker.com/reference/cli/docker/container/run/). Your Docker root is ext4, so per-container disk quotas cannot be enforced as is.
- `[DOCS]` + `[RAN]` Your default buildx builder cannot export a registry cache (see section 3). M1 needs a `docker-container` builder or a standalone `buildkitd`.

## 9. Inputs for Gate B (not decisions yet)

1. Three claim designs seen: Runbot self-claim with `SKIP LOCKED` (2020 to 2026), Runbot central assigner (2026), GitLab optimistic lock + 409. All prevent double assignment; none can undo a zombie's side effects. Open question: attempt-number column (spec 5.2.3) vs new row per attempt (GitLab).
2. Webhook as doorbell + fetch from Git as truth (Runbot) gives idempotency almost for free. Our spec stores the delivery, which still makes sense for audit and replay.
3. Lease timing: GitLab writes the heartbeat at most every 15 min and reaps after 30 min. Our target "recovered within 2 x lease TTL" points to a much shorter TTL (tens of seconds). That means more database writes; fine at our scale.
4. Supersede: Runbot cancels lazily (flag `killable`, kill only when slots are needed) and protects builds shared through dedup. Our spec says cancel queued and running builds. Eager vs lazy is a Gate B choice.
5. Log ordering: Runbot and GitLab both use byte offsets; our spec says sequence numbers. Gate B choice.

## 10. Not verified in this session

- Vercel and Render internals (build isolation, scheduling, routing, artifact storage): not in their docs; not claimed.
- Runbot: any reassignment of a dead host's builds (none found); whether the central assigner also relies on Odoo's REPEATABLE READ serialization errors for safety.
- GitLab: all callers of `tick_runner_queue` (only the definition was read); whether long polling is on by default in Omnibus or Helm installs.
- The Docker embedded DNS address (`127.0.0.11` is commonly cited); not in the page I read.
- GitHub App vs OAuth App: not researched yet (Gate B).
- gVisor, Kata, Firecracker: outside Gate A.
