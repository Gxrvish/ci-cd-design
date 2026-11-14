# Codebase map

The code that exists today: what it does, how it runs, and where to look. Updated 2026-11-14, after M1 slice 3c-2.

- This page covers only the code that exists. The full design is in `docs/architecture/overview.md`.
- Update this page in the same delivery whenever a component, a flow or a file's role changes (CLAUDE.md).
- Reading time: about 20 minutes. Sections 1 and 2 are enough for the big picture.

## 1. The big picture

CloudForge turns a Git commit into a running app with a URL. Three programs share the work:

1. **The control plane** (Python, `control-plane/`): an HTTP API and its PostgreSQL database. It decides and
   records: which builds exist, which runner builds which job, how each one ended. It never runs repository code.
2. **The runner** (Go, `cfnode/`): fetches one commit, builds it into an image with buildkitd, and pushes the
   image to the registry. Untrusted repository code runs only in buildkitd's sandbox.
3. **The agent** (Go, same binary, slice 4): starts the image as a container and routes a hostname to it.

Runners and agents call the control plane; it never calls them (ADR-0002, ADR-0005). GitLab Runner works the
same way: it polls its server for jobs (`[SOURCE]` in ADR-0002). Pulling needs no inbound connection to the
build machines, so they can sit behind NAT or a firewall.

```text
  you, curl, Postman
        │  http://cloudforge-console.localhost:8080
        ▼
      nginx ──▶ api (FastAPI) ──▶ PostgreSQL                  built: slices 1 and 2
                 │  /api: dashboard.py, for people
                 │  /v1:  node_api.py, for runners
                 ▲
                 │  HTTP: register, claim, logs, complete
                 │
           cfnode runner                                      built: slice 3 (the program: 3c-2)
                 ├─ runner.Loop: claim a job, run it
                 ├─ runner.Builder: one job, from checkout to complete
                 ├─ api.Client, runner.Log: the API calls
                 ├─ build.Checkout: fetch one commit ◀── Gitea, the local Git host
                 └─ build.Image ──▶ buildkitd ──▶ registry
                                                     │
           cfnode agent: runs the image at a URL ◀───┘        slice 4
```

## 2. What happens when it runs

### Flow A: `make up`

1. `make init` creates the missing secrets in `secrets/`: database passwords and the registry login.
   `make cfnode` builds the runner's program on the host.
2. `docker compose up nginx` starts the api and what it needs, in `depends_on` order:
   1. **postgres**. On its very first start, `infrastructure/postgres/init.sql` creates two roles:
      `cloudforge_owner` owns the schema, and the api uses `cloudforge_app`. It also creates the databases
      `cloudforge` and `cloudforge_test`.
   2. **migrate** runs as the owner: `cloudforge.migrate` applies new `migrations/*.sql` files, then
      `cloudforge.seed` applies every `seeds/*.sql` file. Then it exits.
   3. **api** runs uvicorn with `cloudforge.api:app`. `api.lifespan` opens the connection pool. The
      healthcheck calls `/healthz`.
   4. **nginx** starts once api is healthy, and forwards `cloudforge-console.localhost` to it.
3. `make register` runs once, when `secrets/runner_credential` is missing. It asks the dashboard API for a
   one-time token, and `cfnode register` trades the token for the runner's credential (flow C, calls 4 and 5).
4. `docker compose up` starts the rest:
   1. **buildkitd** and **registry**, on their own network, cf-build;
   2. **gitea**, the local Git host, at `http://127.0.0.1:3000`;
   3. **runner**: `cfnode runner`, with its credential and the registry login as secret files.

### Flow B: one HTTP request

Every route has the same shape. Example: `POST /api/projects/hello/builds` with `{"commit_sha": "3f78…"}`.

1. Nginx matches the host name and passes the request to `api:8000` (`infrastructure/nginx/console.conf`).
2. FastAPI finds the route `dashboard.create_build`. It checks the body against the Pydantic model
   `NewBuild` (422 if it doesn't fit) and resolves `conn: Connection`, a pooled connection (`db._connection`).
3. The route runs SQL in one transaction: insert the build, then its job, both `queued`.
   `db.fetch_one` turns a returned row into a dataclass.
4. A broken constraint becomes 422 or 409 (`dashboard._constraint_errors`). Otherwise FastAPI turns the
   returned `Build` dataclass into JSON, and the connection goes back to the pool.

This is FastAPI's standard shape: a path operation function, a Pydantic model that validates the body, and
dependencies that FastAPI injects by parameter type. The SQL is written by hand, with bound parameters,
because there is no ORM (ADR-0014).

### Flow C: a build's life, end to end

| # | Who | Call | Code | What changes |
|---|---|---|---|---|
| 1 | you | `POST /api/orgs/default/repositories` | `dashboard.connect_repository` | a `git_repository` row |
| 2 | you | `POST /api/orgs/default/projects` | `dashboard.create_project` | project, branch, environment |
| 3 | you | `POST /api/projects/{slug}/builds` | `dashboard.create_build` | build and job, both queued |
| 4 | `make register` | `POST /api/orgs/default/runner-tokens` | `dashboard.create_runner_token` | a registration token (its hash) |
| 5 | `cfnode register` | `POST /v1/runners/register` | `node_api.register_runner` | token spent; a runner row |
| 6 | runner | `POST /v1/jobs/claim` | `node_api.claim_job` | job running, attempt 1; build running |
| 7 | runner | none: git and buildkitd | `build.Checkout`, `build.Image` | an image in the registry |
| 8 | runner | `POST /v1/jobs/{id}/attempts/{n}/logs` | `node_api.append_log` | `log_chunk` rows |
| 9 | runner | `POST /v1/jobs/{id}/attempts/{n}/complete` | `node_api.complete_job` | job and build succeeded, with the digest |
| 10 | you | `GET /api/builds/{id}` and `.../attempts/{n}/log` | `get_build`, `read_log` | nothing |

- Calls 4 and 5 happen once, in `make up`. The runner makes calls 6 to 9 for every build. The tests and Postman
  can still make them by hand.
- A failed build ends at call 9 too, with a reason in place of the digest: `step_failed:clone`,
  `step_failed:build` or `timeout`.
- Slice 4 adds the step after 9: the agent deploys the image.

### Flow D: inside the runner

1. `cfnode runner` (`cmd/cfnode/main.go`, `serve`) reads its settings and secret files, puts the parts
   together, and calls `runner.Loop`. Ctrl-C or `docker stop` cancels the loop's context.
2. `Loop` takes a free slot, then calls `api.Client.Claim`, a POST to `/v1/jobs/claim`. For a job, it starts a
   goroutine that runs `Builder.Execute(job)`.
3. `Execute` starts the log upload in a second goroutine. Then it checks the commit out into a new folder
   (`build.Checkout`), and builds and pushes it (`build.Image`), both within the claim's time limit.
4. During the build, buildkitd calls back into the runner through the session. It asks for the build's files,
   and for the registry account (`build.Login`).
5. The build writes its output into a `runner.Log`. Its `Upload` sends the output as numbered chunks, at least
   once per second (`api.Client.AppendLog`).
6. `Execute` deletes the folder and completes the job (`api.Client.Complete`): with the digest, or with a reason,
   which it also writes into the log. It sends the completion again until an answer arrives.
7. Two cases end without a completion: the API refuses a log chunk (fenced), or the runner is stopping. The
   attempt is dropped, and its job stays `running` until M2's leases (`docs/stubs.md`).

### Flow E: `make test`

1. **Go, on the host:** gofmt, `go vet`, then `go test -race`. The loop, log and executor tests run on a fake
   clock (`testing/synctest`); the checkout tests use local Git repositories.
2. **Python, in the test image:** ruff, then pytest against `cloudforge_test`. A test that takes the fixture
   `client` gets an emptied, freshly migrated database, and the app, called in-process.
3. **BuildKit, in the test-build container,** which has no network: the compiled Go test builds
   `FROM busybox`, pushes the image and checks its digest.

## 3. File guide

For each important file: what it owns, what it talks to, why it is built this way, and when to open it.

### Control plane: `control-plane/`

| File | Owns | Talks to | Why this way | Open it when |
|---|---|---|---|---|
| `cloudforge/api.py` | the FastAPI app, the pool's lifetime, `/healthz` | `dashboard`, `node_api`, `db` | async, so idle log streams (M4) stay cheap | the API won't start, or `/healthz` says 503 |
| `cloudforge/db.py` | connection settings, the `Connection` dependency, `fetch_one` | every route module, PostgreSQL | no ORM: SQL by hand, rows become dataclasses (ADR-0014) | a query maps wrongly, or a connection fails |
| `cloudforge/dashboard.py` | `/api`: repositories, projects, builds, runner tokens, logs | `db`, `credentials` | the schema checks formats; its errors become 422 or 409 | a dashboard call answers wrongly; you add one |
| `cloudforge/node_api.py` | `/v1`: register, claim, logs, complete; the three secrets | `db`, `credentials`; runners call it | the claim is one transaction with the runner's row locked | a runner gets 204, 401 or 409 it shouldn't |
| `cloudforge/credentials.py` | random secrets, stored as SHA-256 | `dashboard`, `node_api` | shown once; the database never holds them in plain text | you add a token type |
| `cloudforge/migrate.py` | applying `migrations/*.sql`, once each, in order | `db` (owner role), `migrations/` | checksums and an advisory lock, no Alembic (ADR-0014) | `make up` stops at migrate |
| `migrations/0001_initial.sql` | the whole schema: tables, constraints, indexes, grants | every query | constraints guard the rules, even against buggy code | you need a column's meaning or a rule |
| `cloudforge/seed.py`, `seeds/*.sql` | seed data: what every installation needs, the organization `default` until M4 | `db` (owner role) | re-run on every start, so each file is safe to repeat; no ledger (ADR-0014, data kinds) | you wonder where `default` comes from |
| `tests/conftest.py`, `tests/calls.py` | fixtures (empty database, app in-process), shared calls | `api`, `migrate`, `db` | each test starts from nothing | a test fails before its first assert |
| `tests/test_*.py` | one file per module | the module it tests | the tests are the executable spec | you want to know how a call should behave |

`Dockerfile` builds one image for migrate and api, with a test stage on top. `pyproject.toml` pins the dependencies.

### Runner: `cfnode/`

| File | Owns | Talks to | Why this way | Open it when |
|---|---|---|---|---|
| `internal/api/claim.go` | the claim's JSON types, `ErrUnauthorized` | `runner`, `build` | mirrors `node_api.Claim`: change both, and `contracts/v1.yaml` | the contract changes |
| `internal/api/client.go`, with `client_test.go` | `Client`: register, claim, logs, complete; `ErrFenced`, `ErrRejected` | the API, over HTTP | one `post` helper for all calls; a refusal wraps an error value that `errors.Is` finds | a call fails, or the API's answer surprises you |
| `internal/runner/logs.go`, with `logs_test.go` | `Log`: build output as numbered chunks; `retry` | `api.Client.AppendLog`, through a `send` function | ADR-0006: once per second, at most 64 KiB, a failed chunk sent again unchanged | a log is missing, cut wrongly, or late |
| `cmd/cfnode/main.go` | the program: `cfnode runner` and `cfnode register`; settings, secret files, stop signals | every package below | the one place that knows all the parts; the packages don't know each other's settings | the runner won't start, or a setting is missing |
| `internal/runner/execute.go`, with `execute_test.go` | `Builder`: one job from checkout to complete; the time limit; the failure reasons | `build` (as two function fields), `api.Client`, `Log` | the engine is passed in, so the tests need neither Git nor buildkitd | a build ends with the wrong result, or never ends |
| `internal/runner/loop.go` | the claim loop: slots, polling with jitter, backoff, stop | `api.Client` as its `Claimer`, `Builder` as its `Executor` | `Claimer` and `Executor` are interfaces, so tests use fakes | a runner claims too much or too little, or won't stop |
| `internal/runner/loop_test.go` | 6 acceptance tests on a fake clock | `loop.go` | a minute of polling takes milliseconds | you change the loop |
| `internal/build/build.go` | `Checkout`, `Login`, `Image` | git, buildkitd (Unix socket), the registry through buildkitd | the runner talks to buildkitd only, never to Docker (ADR-0008) | a checkout, build or push fails |
| `internal/build/build_test.go` | checkout tests; the BuildKit test | `build.go`; buildkitd and the registry | the BuildKit test needs the stack, so `make test` runs it | you change the build engine |

`go.mod` names the module `cloudforge/cfnode` and pins BuildKit v0.34.2. `Dockerfile` is the runner's image:
Alpine, git, and the binary that `make cfnode` built. The task sheet `docs/you-build-it/m1-claim-loop.md` walks
through `loop.go` block by block.

### Wiring

- `docker-compose.yml`: every service, network, volume and secret. `docs/operations/local-setup.md` explains each one.
- `Makefile`: `init`, `up`, `down`, `register`, `cfnode`, `test`, `test-go`, `clean`.
- `infrastructure/`: Nginx's site for the console, PostgreSQL's first-start script, and the buildkitd image (uid 61000).
- `contracts/v1.yaml`: the node API contract (OpenAPI). `postman/`: the collection for trying the API by hand.

## 4. The data a build creates

```text
organization ─┬─ git_repository ─── branch
              │        │               │
              └────── project ─── environment     (one production environment per project, for now)
                                       │
                                     build ─── job ─── log_chunk    (per attempt, numbered by seq)
                                                │
registration_token ──spent by──▶ runner ◀───────┘ job.runner_id, while the job runs
```

- `build.state` and `job.state`: queued, running, then succeeded, failed or cancelled (`docs/architecture/state-machines.md`).
- `job.attempt` grows by 1 with each claim. `job.token_hash` belongs to the current attempt.
- `runner.pool` comes from its registration token. A trusted runner takes only trusted builds, and an untrusted runner only untrusted ones.

## 5. Concepts, in the order you meet them

| Term | Means | Where |
|---|---|---|
| Dependency injection | FastAPI calls a function for a parameter and passes in its result | `db.Connection`, `node_api.Secret` |
| Claim | the runner asks for one job; the API hands out one and marks it running, in one transaction | `node_api.claim_job` |
| Attempt | one try of a job; each claim adds 1 | `job.attempt` |
| Lease | how long a claim holds its job without a heartbeat: 30 s, enforced from M2 | `job.lease_expires_at` |
| Fencing | refusing calls from an attempt that no longer owns its job | `node_api._job` |
| Three secrets | registration token, then credential, then one job token per attempt | `node_api` module docstring |
| BuildKit, buildkitd | the build engine behind `docker build`, run here as its own rootless daemon | `build.Image` |
| Session | callbacks that buildkitd makes into the runner during a build | `build.Login` |
| Image digest | the SHA-256 of the image's manifest: its exact identity. A tag can move; a digest can't | `complete_job` |
| Goroutine, channel, context | Go's thread, queue, and stop signal | `loop.go` and its task sheet |
| Log chunk | a piece of an attempt's output, numbered by `seq` from 0. The API stores each `seq` once, so a resend is harmless | `runner.Log`, `node_api.append_log` |
| Sentinel error | an error value to check for, like an exception class: `errors.Is(err, api.ErrFenced)` | `internal/api/client.go` |
| Time limit | a context that cancels itself after the claim's `timeout_s`; git and the build stop with it | `Builder.build` |
| Dropped attempt | an attempt that ends without a completion: it was fenced, or its runner is stopping | `Builder.run` |
| Function value | a struct field that holds a function, like a Python attribute that holds a callable | `Builder.Checkout`, `Builder.Image` |

## 6. Debugging: symptom, then where to look

| Symptom | Look at |
|---|---|
| `make up` stops at migrate | `docker compose logs migrate`. "changed after it was applied": an applied file was edited. Undo the edit; add a new file |
| `/healthz` answers 503 | `docker compose ps` (is postgres healthy?), then the secrets in `db.py` |
| 422 with a constraint name in `msg` | that constraint in `migrations/0001_initial.sql` |
| a claim answers 204, though a build is queued | the claim's conditions: organization, pool against `build.trusted`, labels, capacity, paused runner |
| 401 or 409 on logs or complete | `node_api._job`: whose token, which attempt; `_require_running`: is the attempt over? |
| a build stays `queued` | `docker compose ps runner`, then `docker compose logs runner`: one line per job started, completed or dropped |
| the runner exits with "unauthorized" | its credential isn't in this database. Delete `secrets/runner_credential`, then `make up` registers again |
| a build stays `running` for good | its runner stopped during the build, and M1 has no leases (`docs/stubs.md`). The runner's log says "job dropped" |
| a build fails | its log ends with a `[CloudForge: ...]` line: the step, and git's or BuildKit's own error |
| a push gets 401 | `docker compose logs buildkitd`; `Login.Host` must match the registry in the image name |

## 7. Status

- **Built:**
  - the control-plane API for M1 (slices 1 and 2);
  - the runner (slice 3): claim loop, build engine, API client, log upload, executor, and the `cfnode` program
    in its container. A commit pushed to Gitea builds end to end.
- **Next:**
  - slice 4: the agent and its routes;
  - slice 5: `make demo`.
- **Shortcuts:** `docs/stubs.md` lists every one, with the milestone that replaces it.
- **Later milestones:** `docs/milestone-plan.md`.
