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
- Go 1.26 on the host, for `make test-go` (gofmt, vet, tests). You stopped the 300 MB Go image on 2026-11-06, so Go tests run on the host.
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
| migrate | control-plane image, once, before api and worker | cf-control | owner password | M1 |
| api | control-plane image | cf-control, cf-edge | app password, KEK, GitHub App files (M3) | M1 |
| worker | control-plane image | cf-control, cf-edge | same as api | M2 (M1 gives it no work) |
| nginx | `nginx:1.31.8-alpine` | cf-edge, plus every `cf-env-*` (the agent connects it) | none | M1 |
| registry | `registry:2.8.3` | cf-build; published `127.0.0.1:5000` | htpasswd file | M1 |
| buildkitd | `infrastructure/buildkit/`: `moby/buildkit:v0.34.2-rootless` with uid 61000; ADR-0009 flags | cf-build | none | M1 |
| runner | cfnode image, uid 61000 | cf-edge; buildkitd's socket on a shared volume | runner credential, registry login | M1 |
| agent | cfnode image, Docker socket, host network | host | agent credential, registry login | M1 |
| gitea | Gitea; published `127.0.0.1:3000` | cf-edge | none | M1 (webhooks from M3) |
| smee | smee-client | cf-edge | channel URL | M3 |
| object storage | chosen in M4 | cf-control | its keys | M4 |

Image versions are pinned in `docker-compose.yml` and the Dockerfiles. Pinned on 2026-11-06:
- `postgres:18.6`, `nginx:1.31.8-alpine` and `python:3.14.10-slim`;
- `moby/buildkit:v0.34.2-rootless` and `registry:2.8.3`.

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
| `init` | idempotent: creates the missing secrets. From slice 3 also the buildkitd state filesystem (sudo, ADR-0009: a 20 GiB file at `/var/lib/cloudforge/buildkit.img`) | M1 |
| `up`, `down` | mount the build filesystem if it isn't mounted (no fstab edit), then build and start; stop, keeping volumes | M1 |
| `register` | create one-time tokens, register the runner and the agent, write their credential files | M1 |
| `demo` | from nothing to a working URL: `init`, `up`, `register`, push the fixture to Gitea, create the project, build, print the URL | M1 |
| `test`, `contracts` | unit and integration tests (Python and Go, in containers); validate `contracts/v1.yaml` | M1 |
| `chaos`, `e2e` | the 10 failure scenarios; the whole workflow | M2, M4 |
| `clean` | `down`, then remove volumes, after asking | M1 |

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
- So the runner needs internet access, and threat #32 applies.
