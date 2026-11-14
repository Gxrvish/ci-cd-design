# Repository layout and local setup

Status: accepted 2026-11-06 (Gate C part 3).

- This page is the plan. M1's first step creates the skeleton: `git init`, the folders below, `Makefile`, `docker-compose.yml`, `.env.example`, `.gitignore`, then the first commit.
- A folder appears when its first file does. No empty packages "for later".

## Repository layout

Spec 18, with one change `[OUR DECISION]`: `cfnode/` instead of `runner/`, because one Go binary plays both roles, runner and agent (overview.md).

```text
.
├── control-plane/        Python: one image for api and worker (ADR-0001, ADR-0014)
│   ├── cloudforge/       thin FastAPI routers, services, SQL modules, worker loops
│   ├── migrations/       0001_initial.sql (the Gate C schema), then forward-only files
│   ├── seeds/            data every installation needs, applied on every start (ADR-0014)
│   ├── tests/            unit tests, and integration tests against real PostgreSQL
│   ├── pyproject.toml    pinned dependencies
│   └── Dockerfile
├── cfnode/               Go: `cfnode runner` and `cfnode agent`
│   ├── cmd/cfnode/
│   ├── internal/         packages, added as each milestone needs them
│   └── Dockerfile
├── frontend/             React dashboard (M4)
├── contracts/v1.yaml     node API contract. Each side's contract tests live with its own tests.
├── fixtures/             sample and hostile apps (spec 10)
├── postman/              a Postman collection of the HTTP API
├── infrastructure/       nginx/, buildkit/, postgres/ (init script), scripts/
├── docs/                 as today, plus stubs.md and learning-log.md from M1
├── secrets/              created by `make init`, git-ignored
├── docker-compose.yml
├── Makefile
├── .env.example          non-secret settings: hostnames, subnet range, ports
├── CLAUDE.md
└── README.md
```

## Prerequisites

- Linux with rootful Docker Engine and the Compose plugin, GNU make, and `sudo` for `make init`.
- Go 1.26 on the host, for `make test-go` (gofmt, vet, tests) and `make cfnode` (the runner's program). You stopped the 300 MB Go image on 2026-11-06, so Go runs on the host.
  - `cfnode/go.mod` needs Go 1.26.8, because BuildKit v0.34.2 does. An older `go` downloads that toolchain once, by itself (`GOTOOLCHAIN=auto`: 72 MB, 240 MB unpacked in `~/go/pkg/mod`) `[RAN]`.
- `htpasswd` (Ubuntu package `apache2-utils`), for `make init`: the registry accepts only bcrypt password hashes.
- `curl`, for `make register`.
- Nothing else: Python and Node run in containers.
- Free: port 8080 on `127.0.0.1` and `::1`, and ports 3000 and 5000 on `127.0.0.1`.
  - For another web port, set `CF_HTTP_PORT` in `.env`.
  - `[RAN]` On this host Apache holds port 80, so 8080 is the default.
- `[RAN]` This host: Compose v5.7.0, GNU make 4.3, and `mountpoint`, `losetup` and `mkfs.ext4` present.

## Hostnames and ports

| What | Address | Used by |
|---|---|---|
| Dashboard, `/api`, `/webhooks` | `http://cloudforge-console.localhost:8080` (ADR-0015) | browser, scripts |
| Apps | `http://<slug>.cloudforge.localhost:8080`, `http://pr-<n>-<slug>.cloudforge.localhost:8080` | browser |
| Gitea | `http://127.0.0.1:3000` | browser, dev only |
| Registry | `127.0.0.1:5000` for pulls; `registry:5000` for pushes | host Docker daemon; buildkitd |
| PostgreSQL | not published: `docker compose exec postgres psql` | labs |

- **The registry has two names.** buildkitd pushes on its own network. The host's Docker daemon pulls through the published port.
  - Digests don't depend on the registry's name `[INFERRED]`. The M1 lab checks this.
  - `[RAN]` This host's Docker daemon allows plain HTTP for `127.0.0.0/8` and `::1/128`, so pulls need no extra config.

## Compose services

| Service | Runs | Networks | Secrets mounted | From |
|---|---|---|---|---|
| postgres | `postgres:18.6` | cf-control | the superuser's and both roles' passwords | M1 |
| migrate | control-plane image, once, before api and worker: migrations, then seeds | cf-control | owner password | M1 |
| api | control-plane image | cf-control, cf-edge | app password, KEK, GitHub App files (M3) | M1 |
| worker | control-plane image | cf-control, cf-edge | same as api | M2 (M1 gives it no work) |
| nginx | `nginx:1.31.8-alpine` | cf-edge, plus every `cf-env-*` (the agent connects it) | none | M1 |
| registry | `registry:2.8.3` | cf-build; published `127.0.0.1:5000` | htpasswd file | M1 |
| buildkitd | `infrastructure/buildkit/`: `moby/buildkit:v0.34.2-rootless` with uid 61000; ADR-0009 flags | cf-build | none | M1 |
| runner | `cfnode/Dockerfile`: Alpine with git and the `cfnode` binary, built on the host; uid 61000 | cf-edge; buildkitd's socket on a shared volume | runner credential, registry login | M1 |
| agent | cfnode image, Docker socket, host network | host | agent credential, registry login | M1 |
| gitea | `gitea/gitea:28.3.0-rootless` with SQLite; sign-up is off, so accounts come from `gitea admin user create`; published `127.0.0.1:3000` | cf-edge | none | M1 (webhooks from M3) |
| smee | smee-client | cf-edge | channel URL | M3 |
| object storage | chosen in M4 | cf-control | its keys | M4 |

Image versions are pinned in `docker-compose.yml` and the Dockerfiles. Pinned on 2026-11-06:
- `postgres:18.6`, `nginx:1.31.8-alpine` and `python:3.14.10-slim`;
- `moby/buildkit:v0.34.2-rootless` and `registry:2.8.3`.

Pinned on 2026-11-14: `gitea/gitea:28.3.0-rootless`, and `alpine:3.23.8` for the runner (the 3.23 line that the BuildKit image uses, with git 2.52).

- **One image, one builder.** migrate and api share the image `cloudforge-control-plane`, and only migrate has a `build:` entry.
  - `[RAN]` Compose labels an image with the service that built it. When both services built the tag, each `up` could swap the image and recreate the api.

## Networks `[OUR DECISION]`

| Network | Members | Why |
|---|---|---|
| cf-control | postgres, migrate, api, worker | only the control plane can reach the database. `internal`: nothing on it reaches the internet |
| cf-edge | nginx, api, worker, runner, gitea, smee | HTTP between components, without the database |
| cf-build | buildkitd, registry | RUN steps share buildkitd's network: they reach the internet and the registry (which needs a login), nothing else (ADR-0012) |
| cf-env-`<id>` | nginx, one environment's apps | created by the agent (ADR-0010) |

- **The agent uses the host network.** It already holds the Docker socket, so it is root-equivalent (threat model, residual risk 5).
  - From the host it can reach every app container for health checks without joining their networks.
  - It calls the API through Nginx on `127.0.0.1`, with the `Host` header set, so no `.localhost` name lookup is involved.
- **The runner never joins cf-control.** It clones hostile repositories, so even a bug in Git can't reach PostgreSQL directly.
  - It calls the api at `http://api:8000`, on cf-edge, and fetches from `http://gitea:3000` `[OUR DECISION]`.
  - It reaches buildkitd only through the socket on their shared volume, and never the registry: buildkitd pushes.

## Secrets

All files live in `secrets/`, which is git-ignored.
- The folder is mode 0700, so on the host only you can open it.
- The files are 0444, so a container's user can read the ones mounted into it.
  - A mounted file keeps its host owner and mode. `[RAN]` With 0400, PostgreSQL's user (uid 999) got "Permission denied".
- Each file is mounted only into the services listed above.

| File | For | Created by |
|---|---|---|
| `pg_superuser_password` | PostgreSQL's own superuser, needed only on its first start | `make init` |
| `kek/k1` | envelope encryption (ADR-0007) | `make init` |
| `pg_owner_password`, `pg_app_password` | the migration role and `cloudforge_app` | `make init`; the postgres init script creates both roles |
| `registry_htpasswd`, `registry_runner`, `registry_agent` | registry accounts, a stub until M8 (ADR-0009) | `make init` |
| `runner_credential`, `agent_credential` | node API credentials | `make register` |
| `github_app.pem`, `github_webhook_secret` | GitHub App (ADR-0011) | you, in M3 |

## Make targets

| Target | Does | From |
|---|---|---|
| `init` | idempotent: creates the missing secrets. From M5 also the buildkitd state filesystem (sudo, ADR-0009: a 20 GiB file at `/var/lib/cloudforge/buildkit.img`; until then a volume, `docs/stubs.md`) | M1 |
| `up`, `down` | build `cfnode`, start the api, run `register`, then start the rest (from M5: first mount the build filesystem if it isn't mounted, no fstab edit); stop, keeping volumes | M1 |
| `register` | once per credential file: ask the dashboard API for a one-time token, and let `cfnode register` trade it for the runner's credential (the agent's from slice 4). `up` runs it | M1 |
| `cfnode` | build `cfnode/bin/cfnode` on the host, without cgo, so that it runs on Alpine | M1 |
| `demo` | from nothing to a working URL: `init`, `up`, `register`, push the fixture to Gitea, create the project, build, print the URL | M1 |
| `test`, `contracts` | unit and integration tests: Python in a container, Go on the host, then cfnode's BuildKit test in the `test-build` container beside buildkitd, with no network (threat 32); validate `contracts/v1.yaml` | M1 |
| `chaos`, `e2e` | the 10 failure scenarios; the whole workflow | M2, M4 |
| `clean` | after asking: `down`, remove the volumes, and delete the runner's credential, which belonged to that database | M1 |

Linters: `ruff` for Python, `go vet` and `gofmt` for Go, both run by `make test`.

## Checked in the M1 internals lab `[RAN]` 2026-11-06

1. **Rootless buildkitd with the ADR-0009 flags** starts, with the overlayfs snapshotter on a volume.
   - A client running as the same uid, with no network of its own, reaches it through the socket on a shared volume.
2. **Plain HTTP push to `registry:5000`** works with this `buildkitd.toml` entry: `[registry."registry:5000"]` and `http = true`.
3. **Two registry names, one image:** pushed as `registry:5000/lab/app:1`, then pulled on the host as `127.0.0.1:5000/lab/app@sha256:3519…`. The digest matched the registry's `Docker-Content-Digest` header.
4. **A RUN step can't raise its limits.** It has no cgroup filesystem at all ("nonexistent directory").
   - On the host it sits in buildkitd's container cgroup, so `--memory` and `--pids-limit` there cover every build.
5. **buildkitd's uid:**
   - The stock image runs as uid 1000, which is `odoo` on this host. RUN-step root mapped to it (`uid_map 0 1000 1`).
   - A 5-line derived image (`sed` on `/etc/passwd` and `/etc/group`, `chown`, `USER 61000`) runs as uid 61000. That is above this host's `UID_MAX` of 60000, so no person has it. RUN-step root then maps to 61000.
   - The runner runs as the same uid, to share the socket.

Also found: **BuildKit fetches registry tokens in its client, which is our runner.**
- `[RAN]` A client with `--network none` failed with "failed to fetch anonymous token ... lookup auth.docker.io".
- `[SOURCE]` moby/buildkit `b9d02e6`, Apache-2.0:
  - buildkitd takes the token URL (the realm) from the registry's challenge (`util/resolver/authorizer.go:219`).
  - When the client's session offers a token authority, the client fetches the token from that realm (`authorizer.go:361-368`; `session/auth/authprovider/authprovider.go:144-157`).
- So with the stock session, the runner needs internet access, and threat #32 applies.

Checked in M1 slice 3 `[RAN]` 2026-11-07, with BuildKit v0.34.2 (`[SOURCE]` moby/buildkit v0.34.2, Apache-2.0):
- The stock session (`buildctl`) offers a token authority for every registry, even with no login (`session/auth/authprovider/authprovider.go:248-310`).
- When a session offers none, buildkitd fetches the token itself (`util/resolver/authorizer.go:201-210, 354-379`). The runner's session (`build.Login`) offers only an account for our registry.
- Run side by side, with no network for the client: `buildctl` failed with "failed to fetch anonymous token ... network is unreachable", and the runner's code built and pushed `FROM busybox:1.37`.
- A trap in the control run: buildkitd reuses a token handler across sessions when their credentials match (`authorizer.go`, `authHandlerNS.get`). The first control passed only because of an earlier build; it failed after a restart.

## Checked with the first real build `[RAN]` 2026-11-14

1. **A commit pushed to Gitea was built end to end** in 3 s: claim, checkout, build, push, 3 log chunks, complete.
   - The registry answered with the same digest that the build recorded.
   - Alpine's git 2.52 fetches one commit by its SHA from Gitea over HTTP, as uid 61000 with no home folder.
2. **The three failures arrive with their reason and a log line:** `step_failed:build` (`RUN exit 3`), `step_failed:clone` (an unknown commit: "not our ref"), and `timeout`.
   - At the time limit (60 s, the smallest that the schema allows), BuildKit showed the step as `CANCELED`. The step's `sleep` was visible in buildkitd while it ran, and gone when the build ended. This answers half of ADR-0008's open check; a runner that disappears is slice 5's break-it.
3. **The runner stops cleanly:** `docker compose stop runner` took 0.2 s while idle, with exit code 0. A refused credential ends it with exit code 1 and the API's message.
4. **`make up` is safe to repeat:** the second run registers nothing and recreates nothing (4.5 s).
5. **`cfnode register` runs on the host** and calls the console address. The static Go binary resolves `cloudforge-console.localhost` to `::1` and `127.0.0.1` here, through systemd-resolved.
