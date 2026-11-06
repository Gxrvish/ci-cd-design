# Progress

## Current position

- Milestone: M1 (walking skeleton). Plan approved, lab done, slice 1 and slice 2a done (2026-11-06). Slice 2b is next.
- M0: done 2026-11-06 (gates, lab, `docs/learning-log.md`). Its explain-back was skipped; the questions stay in the lab sheet.
- Gate A (research): approved 2026-11-05. Report: `docs/research/gate-a.md`.
- Gate B (architecture): approved 2026-11-06, all 3 parts. The part 3 amendments to earlier documents are applied.
  - Part 1: `docs/architecture/overview.md`, ADR-0001 to ADR-0005.
  - Part 2: `docs/architecture/data-model.md`, `state-machines.md`, `failures-and-targets.md`, ADR-0006, ADR-0007.
  - Part 3: ADR-0008 to ADR-0013, `docs/threat-model.md`, `docs/milestone-plan.md`.
- Gate C (technical design): approved 2026-11-06, all 3 parts.
  - **Part 1, schema:** approved 2026-11-06. Now `control-plane/migrations/0001_initial.sql`, checked by `docs/architecture/schema-check.sql`. Its refinements are applied to `data-model.md`.
  - **Part 2, API contracts**, delivered in pieces:
    - **2a, runner API:** approved 2026-11-06. `contracts/v1.yaml`.
    - **2b, agent API:** approved 2026-11-06. Same file.
    - **2c, dashboard API and auth:** approved 2026-11-06. `docs/adr/0015-dashboard-auth.md`, `docs/api/dashboard.md`.
  - **Part 3, repo skeleton and local setup:** approved 2026-11-06. `docs/operations/local-setup.md`.
- ADR-0014 accepted 2026-11-06: no ORM. The control plane uses psycopg 3 and plain SQL migrations.
- M0 internals lab: `docs/labs/m0-container-by-hand.md`, short version (15 min, one terminal). Run by you 2026-11-06. The first sheet (45 min, two terminals) felt awkward, so it was replaced.
- Application code starts in M1, after the M1 design talk and your "go" (spec 21).
- Working rules added 2026-11-06:
  - aim for at most 500 added or changed lines per delivery, for every change; go over only when splitting hurts understanding, and say why;
  - clean code, SOLID, DRY, KISS and YAGNI, strictly.

## Decisions made

- 2026-11-05: Stack profile A (Python/FastAPI control plane, Go runner, React dashboard, no Redis at first).
- 2026-11-05: Host = this machine for all labs: Ubuntu 22.04, kernel 6.8, cgroup v2, rootful Docker 29.9, 8 CPUs, 15.7 GB RAM, 32 GB swap, KVM usable.
  - `sudo` is allowed for labs.
  - Rootless Docker may be installed side by side later.
- 2026-11-05: GitHub integration via a GitHub App. Tunnel: smee.io. Confirmed by ADR-0011.
- 2026-11-05: M9 nodes = this host (node 1) + one local VM (node 2, 2-3 GB RAM). VM tool chosen at M9.
- 2026-11-05: Time budget 6 h/week.
- 2026-11-05: Gate B part 1 approved. ADR-0001 to ADR-0005 accepted.
- 2026-11-06: Gate B part 2 approved. Data model, state machines, failure scenarios and targets, ADR-0006 and ADR-0007 accepted.
- 2026-11-06: Gate B part 3 approved:
  - **Build:** one BuildKit solve per attempt, steps read from vertex names, tests as a stage forced in by a marker file, image tag `<sha12>-b<build>-a<attempt>` (ADR-0008).
  - **BuildKit:** one rootless buildkitd per runner, reached through a Unix socket. Runners belong to one organization and one pool. Registry: htpasswd in M1, job-scoped tokens in M8 (ADR-0009).
  - **Routing:** Nginx with a generated `map` plus reload, one Docker network per environment, mkcert in M8 (ADR-0010).
  - **GitHub:** a GitHub App registered through the manifest flow, preview URL in the commit status, smee with verification always on (ADR-0011).
  - **Isolation and branches:**
    - hardened app containers;
    - internet egress allowed, private ranges blocked in M8 (ADR-0012);
    - development branches build and test but don't deploy;
    - fork PRs run nothing before a per-SHA approval, which never unlocks secrets (ADR-0013).
  - **Plan:** 225 h over 42 weeks (`docs/milestone-plan.md`).
- 2026-11-06: Gate C part 1 approved:
  - the schema in SQL, with text + CHECK instead of enum types and no cascading deletes;
  - a migration role that owns the tables, and the app role `cloudforge_app`.
- 2026-11-06: Gate C parts 2a and 2b approved: the node API contract (`contracts/v1.yaml`).
  - Job calls go to `/v1/jobs/{id}/attempts/{n}/...` with the attempt's token. The claim carries lease timings, limits and the image name.
  - The agent uses `/v1/nodes/me/...`. It reconciles on every poll and probes health every 2 s: healthy after 3 good answers, failed if not healthy within 120 s.
  - Observed reports carry `routed_deployment_ids` and `proxy_error`. Errors use FastAPI's default `{"detail"}` body.
- 2026-11-06: Gate C part 3 approved, which completes Gate C:
  - spec 18's layout, with `cfnode/` for the one Go binary;
  - everything in Compose except the host's Docker daemon;
  - networks cf-control, cf-edge, cf-build and cf-env-`<id>`;
  - the agent on the host network; one registry with a push name and a pull name; Gitea from M1.
- 2026-11-06: Gate C part 2c approved (ADR-0015, `docs/api/dashboard.md`):
  - the dashboard is on its own site, `cloudforge-console.localhost`;
  - server-side sessions in a `SameSite=Strict` host-only cookie; writes need `Sec-Fetch-Site: same-origin` or a matching `Origin`;
  - passwords hashed with standard-library scrypt; API tokens as Bearer;
  - out-of-tenant rows answer 404; a Gitea webhook secret is optional until M3.
  - Amendments applied: ADR-0010 (dashboard host), ADR-0006 (log path by build id), threat model (threats 5, 6 and 31).
- 2026-11-06: ADR-0014 accepted:
  - no ORM: psycopg 3 and `psycopg_pool`, with plain SQL migration files and a small runner;
  - this replaces SQLAlchemy and Alembic from spec 4 (ADR-0001 amended);
  - the reason is cost, not capability: SQLAlchemy can do almost everything we need.

- 2026-11-06: M1 plan approved ("go" = your example `1a 2yes 3yes`):
  - the agent drives Docker with stdlib HTTP over the socket; no SDK, no CLI;
  - the worker container starts in M2;
  - `git init` done. I commit each verified delivery; you review with `git show` ("commit always", 2026-11-06).
- 2026-11-06: M1 lab checks `[RAN]` (`docs/operations/local-setup.md`). buildkitd runs as uid 61000 from a derived image (ADR-0009 amended); new threat 32.

- 2026-11-06: M1 slice 1 (skeleton, PostgreSQL, migrations, `GET /healthz`):
  - the API is async (SSE in M4 keeps many idle connections); one image serves migrate and api;
  - port 8080 by default, because Apache holds 80 on this host (ADR-0015 amended);
  - the migration ledger lives in schema `migration`, out of the app role's reach (ADR-0014 amended);
  - secrets: folder 0700, files 0444; a third password for PostgreSQL's superuser.

- 2026-11-06: M1 slice 2a (dashboard API for M1: repositories, projects, builds):
  - the schema's constraints validate input; their errors become 422 (constraint name in `msg`) or 409;
  - rows are dataclasses that FastAPI also returns as responses (ADR-0014); requests are Pydantic models;
  - pooled connections are in autocommit mode; several writes go inside an explicit transaction.

## Open questions (not blocking)

- **Staging hostnames.** ADR-0010 names production (`<slug>`) and previews (`pr-<n>-<slug>`), but not staging environments. The unique hostname column stops collisions in any case. The naming rule is decided in M7, with the branch type policies.
- **Connecting a GitHub repository (M3).** The API must check that the user can access the GitHub App installation before linking one of its repositories. The schema already lets only one organization own a repository.
- **Registry tokens (M1 slice 3, threat 32).** BuildKit fetches registry tokens in its client, our runner, from a URL the registry chooses. Slice 3 checks that a session without a token authority makes buildkitd fetch them itself, from cf-build.

## M1 plan (approved 2026-11-06)

- **Goal (spec M1):** `make demo` takes the fixture Dockerfile app from nothing to a working URL.
  - Happy path only: no auth, UI, webhooks, leases or retries yet.
- **Order:** the M1 lab first, then 5 slices of at most 500 lines each:
  1. git init, skeleton, PostgreSQL, migrations (ADR-0014), `GET /healthz`.
  2. Control-plane API for M1. Dashboard side: repositories, projects, builds. Runner side: register, claim, logs, complete. The deploy shortcut (ADR-0005) runs inside `complete`'s transaction.
  3. cfnode runner: the claim loop (YOU BUILD IT), then clone, BuildKit build and push.
  4. cfnode agent and Nginx, with the M1 shortcut: stop old, then start new. The real reconciliation loop is M6's YOU BUILD IT.
  5. `make demo` with the fixture app in Gitea. Then break it (kill the runner mid-build), review, explain-back.
- **Decided:** the agent uses stdlib HTTP on the Docker socket; the worker starts in M2; `git init` done.
- **Defaults unless you object:**
  - Clone with the git CLI (ADR-0008: depth 1, no submodules, no LFS).
  - Trigger builds with curl. No CLI.

## Next step

1. Slice 2b, the runner side of the API: registration tokens, register, claim, logs, complete, and the M1 deploy shortcut.
