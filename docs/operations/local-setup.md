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
│   ├── migrations/       0001_initial.sql (today's schema.sql), then forward-only files
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
- Nothing else. Python, Go and Node run in containers; the host's own copies are only for editors.
- Free: port 80 on `127.0.0.1` and `::1`; ports 3000 and 5000 on `127.0.0.1`.
- `[RAN]` This host: Compose v5.7.0, GNU make 4.3, and `mountpoint`, `losetup` and `mkfs.ext4` present.

## Hostnames and ports

| What | Address | Used by |
|---|---|---|
| Dashboard, `/api`, `/webhooks` | `http://cloudforge-console.localhost` (ADR-0015) | browser, scripts |
| Apps | `http://<slug>.cloudforge.localhost`, `http://pr-<n>-<slug>.cloudforge.localhost` | browser |
| Gitea | `http://127.0.0.1:3000` | browser, dev only |
| Registry | `127.0.0.1:5000` for pulls; `registry:5000` for pushes | host Docker daemon; buildkitd |
| PostgreSQL | not published: `docker compose exec postgres psql` | labs |

- **The registry has two names.** buildkitd pushes on its own network. The host's Docker daemon pulls through the published port.
  - Digests don't depend on the registry's name `[INFERRED]`. The M1 lab checks this.
  - `[RAN]` This host's Docker daemon allows plain HTTP for `127.0.0.0/8` and `::1/128`, so pulls need no extra config.

## Compose services

| Service | Runs | Networks | Secrets mounted | From |
|---|---|---|---|---|
| postgres | `postgres:18` | cf-control | both database passwords | M1 |
| migrate | control-plane image, once, before api and worker | cf-control | owner password | M1 |
| api | control-plane image | cf-control, cf-edge | app password, KEK, GitHub App files (M3) | M1 |
| worker | control-plane image | cf-control, cf-edge | same as api | M1 |
| nginx | nginx | cf-edge, plus every `cf-env-*` (the agent connects it) | none | M1 |
| registry | `registry:2` | cf-build; published `127.0.0.1:5000` | htpasswd file | M1 |
| buildkitd | `moby/buildkit:rootless`, ADR-0009 flags | cf-build | none | M1 |
| runner | cfnode image | cf-edge; buildkitd's socket on a shared volume | runner credential, registry login | M1 |
| agent | cfnode image, Docker socket, host network | host | agent credential, registry login | M1 |
| gitea | Gitea; published `127.0.0.1:3000` | cf-edge | none | M1 (webhooks from M3) |
| smee | smee-client | cf-edge | channel URL | M3 |
| object storage | chosen in M4 | cf-control | its keys | M4 |

Image versions are pinned in `docker-compose.yml` when M1 first pulls them. `postgres:18` is already checked (18.1).

## Networks `[OUR DECISION]`

| Network | Members | Why |
|---|---|---|
| cf-control | postgres, migrate, api, worker | only the control plane can reach the database |
| cf-edge | nginx, api, worker, runner, gitea, smee | HTTP between components, without the database |
| cf-build | buildkitd, registry | RUN steps share buildkitd's network: they reach the internet and the registry (which needs a login), nothing else (ADR-0012) |
| cf-env-`<id>` | nginx, one environment's apps | created by the agent (ADR-0010) |

- **The agent uses the host network.** It already holds the Docker socket, so it is root-equivalent (threat model, residual risk 5).
  - From the host it can reach every app container for health checks without joining their networks.
  - It calls the API through Nginx on `127.0.0.1`, with the `Host` header set, so no `.localhost` name lookup is involved.
- **The runner never joins cf-control.** It clones hostile repositories, so even a bug in Git can't reach PostgreSQL directly.

## Secrets

All files live in `secrets/`, git-ignored, with mode 0400. Each is mounted only into the services listed above.

| File | For | Created by |
|---|---|---|
| `kek/k1` | envelope encryption (ADR-0007) | `make init` |
| `pg_owner_password`, `pg_app_password` | the migration role and `cloudforge_app` | `make init`; the postgres init script creates both roles |
| `registry_htpasswd`, `registry_runner`, `registry_agent` | registry accounts, a stub until M8 (ADR-0009) | `make init` |
| `runner_credential`, `agent_credential` | node API credentials | `make register` |
| `github_app.pem`, `github_webhook_secret` | GitHub App (ADR-0011) | you, in M3 |

## Make targets

| Target | Does | From |
|---|---|---|
| `init` | idempotent setup. `.env` from `.env.example`; missing secrets; the buildkitd state filesystem (sudo, ADR-0009: a 20 GiB file at `/var/lib/cloudforge/buildkit.img`) | M1 |
| `up`, `down` | mount the build filesystem if it isn't mounted (no fstab edit), then build and start; stop, keeping volumes | M1 |
| `register` | create one-time tokens, register the runner and the agent, write their credential files | M1 |
| `demo` | from nothing to a working URL: `init`, `up`, `register`, push the fixture to Gitea, create the project, build, print the URL | M1 |
| `test`, `contracts` | unit and integration tests (Python and Go, in containers); validate `contracts/v1.yaml` | M1 |
| `chaos`, `e2e` | the 10 failure scenarios; the whole workflow | M2, M4 |
| `clean` | `down`, then remove volumes, after asking | M1 |

Linters: `ruff` for Python, `go vet` and `gofmt` for Go, both run by `make test`.

## Checked in the M1 internals lab, not before

1. Rootless buildkitd with the ADR-0009 flags. Its socket is shared with the runner (same uid).
2. buildkitd pushing over plain HTTP to `registry:5000`. This needs a registry entry in `buildkitd.toml` `[INFERRED]`.
3. A push to `registry:5000`, then a pull by digest from `127.0.0.1:5000`, giving the same image.
4. A RUN step can't raise its own limits: `echo max > /sys/fs/cgroup/memory.max` must fail. In the M0 lab, the same write succeeded in the hand-made container (`docs/learning-log.md`).
5. buildkitd's uid. On this host, uid 1000 is the login user (`docs/progress.md`, open questions).
