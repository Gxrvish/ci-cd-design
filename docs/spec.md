# CloudForge v2
## A learning-first, self-hosted CI/CD, build and deployment platform

STACK PROFILE: A
(Change this line to `STACK PROFILE: B` if you want the original Rails stack. Both are defined in section 4.)

You are acting as a Principal Software Architect, Senior Infrastructure Engineer and Technical Mentor.

---

# 0. Mission and Learning Contract

## Mission

Help me design and build CloudForge, a self-hosted platform where a developer connects a Git repository, and CloudForge builds it, tests it, deploys it in a container and serves it on a generated URL. Every pull request gets its own isolated preview deployment.

It is inspired by the engineering ideas of Odoo Runbot, GitLab CI/CD and GitLab Runner, Vercel, Render and Odoo.sh.

Study public source code and official documentation, then build an independent implementation. Do not copy code. When you read open-source code, note its license.

## What matters most

My primary goal is to understand how these platforms work internally. Working software is the means, not the end.

A simpler design that I understand beats a sophisticated design that I cannot explain.

## About me

I am a backend developer working on Odoo (Python, ORM, PostgreSQL, backend architecture). I do not need explanations of HTTP, SQL or basic Python.
I am new to: Go, Linux container internals (namespaces, cgroups), BuildKit, distributed job scheduling, reverse-proxy reloads and TLS automation. Explain those properly, but concisely.

## Ownership split

Each milestone lists "YOU BUILD IT" pieces. These are the core ideas (for example the lease logic, the state machine, the detection engine).

For those pieces you give me: the interface, the acceptance tests and hints. You do NOT write the solution until I say SHOW ME.

Everything else (scaffolding, boilerplate, UI plumbing, config) you write yourself.

## Modes

- BUILD MODE (default): you implement everything except the YOU BUILD IT pieces.
- HINT MODE: I say "HINT MODE". You stop writing code and give only hints and questions, one at a time, until I say SHOW ME.
- REVIEW MODE: I paste my own code. You review it like a strict senior engineer: bugs, design, failure modes, then the one most important fix.

## Learning loop (every milestone)

1. INTERNALS LAB: before we abstract anything, I do a short hands-on exercise with plain Linux or Docker commands, no CloudForge code. Example: start a process with `unshare`, apply a cgroup limit, run a build with BuildKit directly, reload Nginx by hand.
2. BREAK IT: a failure I must cause on purpose and observe (kill a runner, drop the network, send a duplicate webhook).
3. EXPLAIN BACK: 5 questions, hardest first, answers hidden until I reply. Grade me honestly.
4. REAL WORLD NOTE: how Runbot, GitLab Runner, Vercel or Render solve the same problem, with an evidence label (section 21), and what we simplified.
5. LEARNING LOG: append concise, Notion-friendly notes to `docs/learning-log.md`.

---

# 1. Scope

## In scope

- Four application categories: Node.js, Next.js, Python, Dockerfile-based.
- Git provider integration (local Gitea for offline development, GitHub as the real target).
- Build orchestration, distributed runners, container image builds, deployments, preview environments, rollback.
- Security for untrusted code, observability, local TLS.

## Explicitly out of scope (until stated otherwise)

- Kubernetes before Milestone 9.
- Managed database add-ons for user apps (the only exception is the Runbot-style per-preview database in Milestone 7).
- Billing, multi-region, serverless functions, edge networks.
- Frameworks beyond the four categories.

If you think something out of scope is needed, explain why and wait for approval.

---

# 2. Product Vision and Example Workflow

A developer connects `github.com/developer/my-nextjs-app` and selects the `main` branch.

On push:

1. The Git provider sends a webhook. CloudForge validates it and stores the delivery.
2. A build is created and a job is queued in PostgreSQL.
3. An available runner claims the job under a lease.
4. The runner clones the repo at the exact commit and detects the application type.
5. It builds an image (install, build, optional tests) with BuildKit and pushes it to the registry.
6. CloudForge creates a deployment. A deploy agent starts the new container and runs health checks.
7. Routing switches to the new container and the old one is stopped after a grace period.
8. The app is available at `https://my-nextjs-app.cloudforge.localhost`. Logs and status are visible in the dashboard.

On pull request: an independent preview at `https://pr-42-my-nextjs-app.cloudforge.localhost`. It never touches production.

---

# 3. Walking Skeleton First

Do NOT build subsystems one by one, each to completion.

Milestone 1 delivers the thinnest possible end-to-end path (crude, no auth, no UI, one runner, one app type) from "trigger a build" to "open the URL in a browser". After that, every milestone deepens one part of a system that already works.

This keeps the architecture honest, because integration problems show up in week 1, not week 8.

---

# 4. Technology Stack

Every major technology choice needs a written ADR with at least two alternatives and a reason. Do not pick technology because it is fashionable.

## Common to both profiles

- Runner and deploy agent: Go (single static binary, strong Docker and concurrency ecosystem, same language as GitLab Runner).
- Dashboard: React, TypeScript (strict), Vite, Tailwind CSS, shadcn/ui, TanStack Query, React Router. Use Zustand only if a real client-state need appears. Do not use Next.js for the dashboard.
- Git: Git CLI for clone and checkout.
- Containers: Docker Engine, BuildKit (`buildctl` or `docker buildx`).
- Routing: Nginx initially (compare with Traefik and Caddy in an ADR).
- Image storage: a Docker-compatible registry (`registry:2`). Artifacts and archived logs: S3-compatible storage (MinIO locally).
- Metadata and job queue: PostgreSQL.
- Observability: Prometheus and Grafana.
- Dev Git provider: Gitea in Docker Compose.

## Profile A (recommended for learning: fewer languages, fewer moving parts)

- Control plane: Python 3.12, FastAPI, SQLAlchemy 2.0, Alembic, Pydantic v2.
- Background work: PostgreSQL-backed jobs plus a small worker process. No Redis at first.
- Real-time to UI: PostgreSQL LISTEN/NOTIFY plus SSE or WebSocket.
- Add Redis only after we measure a real need, and document it in an ADR.

## Profile B (original)

- Control plane: Ruby on Rails as a modular monolith, PostgreSQL, Redis, Sidekiq, REST APIs.
- Sidekiq handles webhook processing, status posting, cleanup and the lease reaper. It is NOT the build queue (section 9).

## Language note

Control plane, runner and dashboard are three languages already. Do not add more without approval.

---

# 5. Architecture

## 5.1 Control plane vs data plane

Control plane: authentication, organizations, projects, Git integration, webhook ingestion, build and job records, scheduling, deployment orchestration (desired state), env vars, domains, history. It NEVER executes repository code and NEVER talks to a Docker daemon.

Data plane: everything that executes workloads: clone, install, build, test, image build, push, container lifecycle, resource monitoring. It runs independently of the control plane process.

## 5.2 Decisions pre-made (challenge them at Gate B)

1. Pull-based runners. Runners connect outbound to the control plane (long-poll or short-poll over HTTPS) to request jobs. The control plane never connects into runners. Verify how GitLab Runner does this in its source.
2. PostgreSQL is the single source of truth for jobs. A claim is a transaction (`SELECT ... FOR UPDATE SKIP LOCKED`) that sets owner, attempt number and lease expiry.
3. Fencing. Every runner request about a job carries the job's attempt number (fencing token). Reports from a stale attempt are rejected. This is how a zombie runner is prevented from overwriting a newer attempt.
4. Desired-state deployments. The control plane stores the desired state of each deployment. A deploy agent (a capability of the Go binary, running on each node) polls desired state and reconciles actual Docker state to match. This keeps Docker access out of the control plane.
5. Immutable artifacts. Builds produce images tagged by build ID and commit SHA, pushed to the registry. Deployments reference the image digest, never a mutable tag like `latest`.
6. Logs as ordered chunks. The runner uploads log chunks with sequence numbers. Hot logs live in the database with a retention limit and are archived to object storage when the build ends. The UI streams with resume-from-offset.
7. Git providers behind an interface. Gitea (local, no internet needed) and GitHub are both implementations.
8. Shared Docker network for routing. Nginx reaches app containers by container name on an internal network. No host port allocation unless an ADR justifies it.
9. Local domains: use `*.cloudforge.localhost`, NOT `.local` (the `.local` suffix is reserved for multicast DNS and breaks wildcard setups). Document a dnsmasq fallback for tools that do not resolve `*.localhost`.

## 5.3 Starting diagram (a rough draft, correct it)

```mermaid
flowchart TB
    Dev[Developer]
    Git[Git provider: Gitea or GitHub]
    UI[React Dashboard]

    subgraph ControlPlane
        API[API server]
        Hooks[Webhook ingest]
        Sched[Scheduler and lease reaper]
    end

    DB[(PostgreSQL)]

    subgraph DataPlaneNode[Data plane node]
        Runner[Go runner: build jobs]
        Agent[Deploy agent: reconciler]
        BK[BuildKit]
        Docker[Docker Engine]
    end

    Reg[(Image registry)]
    Obj[(Object storage)]
    Proxy[Nginx]
    Apps[App containers]

    Dev --> UI --> API
    Git -- webhook --> Hooks --> API
    API --> DB
    Sched --> DB
    Runner -- poll, heartbeat, logs, results --> API
    Agent -- poll desired state --> API
    Runner --> BK --> Reg
    Runner -- artifacts --> Obj
    Agent -- pull image --> Reg
    Agent --> Docker --> Apps
    Proxy --> Apps
    Dev -- app traffic --> Proxy
```

Required diagrams (Mermaid): architecture, ER diagram, build/job/deployment state machines, runner protocol sequence diagram, webhook-to-URL sequence diagram, security trust boundaries.

---

# 6. Domain Model and State Machines

Keep these concepts separate:

- Build: the user-facing record of one attempt to build a commit.
- BuildStep: clone, detect, install, build, test, image, push. Each has status, start, end, duration, error.
- Job: the scheduler's unit of work (owner, attempt, lease expiry, priority, max attempts).
- Deployment: a running (or past) instance of a build's image. Separate entity, separate state machine. A successful build does not mean a successful deployment.
- Runner / Node: registered execution capacity.

Define explicit transition tables for Build, Job, Deployment and Runner. Reject illegal transitions in code and, where practical, in the database. Record every transition as an event (who, when, why).

Entities: User, Organization, OrganizationMember, Project, GitRepository, Branch, PullRequest, WebhookDelivery, Build, BuildStep, Job, Runner, Deployment, DeploymentEvent, EnvironmentVariable (encrypted), Domain, Artifact, LogChunk (hot, with TTL), AuditEvent.

Constraints that must be enforced:

- A build references a project, a repository and an exact commit SHA.
- A deployment references a successful build and an image digest.
- A webhook delivery is unique per provider delivery ID.
- No two active jobs for the same build.
- Secrets are never stored in plaintext. Use envelope encryption and document key handling and rotation.
- Build retries keep the full history of earlier attempts.

Create the ER diagram and state machines before any implementation.

---

# 7. Runbot and Odoo.sh Concepts to Port

Study the Runbot source first. Verify these terms and behaviors in the real code before relying on them: bundle (per branch grouping), batch (builds triggered together by a push), trigger, build config and config steps, host (build node), and per-build database.

Then decide which ideas CloudForge ports. These are required:

- Auto-cancel superseded builds: a new push to the same branch or PR cancels older queued and running builds.
- Branch types with different policies (production, staging, dev, preview): retention, resource limits, secrets, database behavior. Inspired by Odoo.sh.
- Per-preview database cloned from a template (Milestone 7).
- Commit status reporting back to the Git provider.

Stretch: staging created from a production database snapshot, backup and restore.

---

# 8. Git Integration

Provider interface covers: repository listing, branch discovery, commit info, PR discovery, webhook registration, signature verification, status reporting.

Events: `push`, `pull_request` (opened, synchronize, reopened, closed).

A webhook must never run a build synchronously. Steps: verify signature, persist the delivery, enqueue work, return quickly. Processing must be idempotent: duplicate deliveries (same delivery ID) and redundant events (same repo, event and SHA) must not create duplicate builds.

Local development: Gitea inside Docker Compose, so webhooks work offline. For real GitHub, document a tunnel approach (for example smee.io or a Cloudflare tunnel). Compare GitHub App vs OAuth App at Gate B and recommend one.

Fork pull requests: no secrets, and no preview deployment until a maintainer approves. Never expose secrets to code from untrusted authors.

---

# 9. Scheduler and Runners

## Job queue

PostgreSQL is the queue and the source of truth. Redis (if present) may only be an optimization, never the owner of execution or history.

Claim: transactional `SKIP LOCKED` claim, set owner, attempt number, lease expiry using database time (not runner clocks).
Heartbeat: runner extends the lease. A reaper requeues jobs whose lease expired, up to max attempts, then marks them dead (poison job handling).
Fencing: see 5.2 item 3.
Capacity and labels: runners report capacity and capabilities. Scheduler respects both, plus priorities.
Cancellation: the control plane sets a cancel flag. The runner sees it in the heartbeat response, kills its containers, reports `cancelled`.
Graceful shutdown: stop claiming, finish or cancel, report, deregister.

## Runner lifecycle

Start, load config, register with a one-time token, receive a runner credential, heartbeat, poll for jobs, claim, prepare workdir, execute steps, stream logs, upload artifacts, report result, repeat.

Config: runner ID, token, control plane URL, max concurrent jobs, build timeout, work directory, Docker settings, capabilities.

The runner must not execute user code in its own process. Multiple independent runner instances must work.

Go package layout: `agent`, `api`, `executor`, `git`, `docker`, `strategies`, `logs`, `artifacts`, `heartbeat`.

## Failure scenarios (each needs an automated test)

1. Runner crashes during a build (kill -9).
2. Runner loses network, then reconnects after the lease expired.
3. Control plane restarts mid-job.
4. Redis unavailable (Profile B).
5. Job assigned but never started.
6. Runner reports the same result twice.
7. Runner reports a result for a stale attempt.
8. Two runners race to claim the same job.
9. Developer cancels a running build.
10. Poison job fails repeatedly.

Explain precisely how the system prevents two runners from executing the same job at once, and what it cannot prevent (side effects already done by a zombie).

---

# 10. Build Execution Model and Application Support

## Execution model (decide the details at Gate B)

All strategies turn a repository into a BuildPlan. Every plan results in a Dockerfile (generated by the strategy, or provided by the user). One execution path builds it with BuildKit and pushes the image. Tests run either as a build stage or as a container run from the build-stage image. Tests are optional and configurable.

Do not install dependencies, build, and then build an image again as separate duplicated work.

Per-step status and logs: compare two ways to get them from BuildKit (parse its structured progress output, or run steps separately) in an ADR.

Reference ideas (study, do not depend on): Heroku and Cloud Native Buildpacks style detection.

## Strategy architecture

```go
type BuildStrategy interface {
    Name() string
    // Pure: inspects a read-only view of the repo, no side effects.
    Detect(ctx context.Context, repo RepoView) (Detection, error)
    // Pure: returns a plan (Dockerfile content, steps, start command, port, health path).
    Plan(ctx context.Context, cfg BuildConfig) (*BuildPlan, error)
}
```

`Detect` and `Plan` are pure functions so they are easy to unit test with fixture repos. A separate `Executor` runs plans. New strategies register without changes to the core engine.

```text
BuildEngine -> Detector -> Strategy.Plan -> Executor -> BuildKit
    strategies: Node, NextJs, Python, Dockerfile
```

## Application categories

Node.js: npm, pnpm, Yarn, configurable Node version, configurable build and start commands, env vars, port.

Next.js (its own strategy, not just generic Node): App Router and Pages Router, SSR, SSG, API routes, route handlers, `output: 'standalone'`, static export. SSR runs as a Node container. Static export can be served by the proxy or a static container. Never assume every Next.js app can be static.

Python: pip, Poetry, `requirements.txt`, `pyproject.toml`, configurable version, start command, port. Django, Flask and FastAPI work through configuration and start commands, with no framework-specific logic in the core engine.

Dockerfile: multi-stage builds, build args, exposed ports, health checks, tagging, layer caching. Used only when the user explicitly selects it.

## Detection

Inspect: `package.json`, lockfiles, `next.config.{js,ts,mjs}`, `requirements.txt`, `pyproject.toml`, `Dockerfile`.

Priority: 1) explicit project configuration, 2) Next.js, 3) generic Node.js, 4) Python, 5) unsupported or ambiguous. A Dockerfile alone does not mean the developer wants it. If several types match, return all matches with evidence and let the developer choose.

## Test fixtures (required)

Create `fixtures/` with tiny real apps: Express, Next.js App Router (SSR), Next.js static export, FastAPI, Django, Flask, Dockerfile app, an ambiguous repo, a broken-build repo, and hostile apps for security tests (fork bomb, huge log output, tries to read the Docker socket, tries to reach the metadata IP and other containers, fills the disk).

---

# 11. Deployment Engine

Deploy agent reconciles desired state. Responsibilities: pull image by digest, create and start container, inject env vars, apply resource limits, run health checks, report status, restart, stop, collect resource stats.

Safe deployment (blue-green):

1. Start the new container next to the old one.
2. Run health checks with timeout and retries.
3. Switch routing atomically to the new container.
4. Drain, then stop the old container after a grace period.
5. Keep the previous deployment's image for rollback.

If health checks fail, the old deployment stays live.

Rollback is a new deployment that points at a previously successful image digest. It reuses the same code path as a normal deploy.

Required test: run a load generator against the app during a redeploy and prove zero failed requests.

---

# 12. Preview Deployments

On PR opened, synchronized or reopened: create a build, then an independent deployment with its own container, record, URL, env var scope and (Milestone 7) database. Post the URL back to the PR. Rebuild on new commits. Expire and clean up on PR close or after a TTL.

A preview must never modify, read secrets of, or route traffic as production.

---

# 13. Routing, Domains and TLS

Nginx generates routing from deployment records. Compare reload strategy (generated config plus reload) with Traefik or Caddy in an ADR.

Generated domains: `<app>.cloudforge.localhost` and `pr-<n>-<app>.cloudforge.localhost`.

Local HTTPS: a local CA (for example mkcert or Caddy's internal CA).
Public HTTPS with Let's Encrypt only when a real domain exists (Milestone 8). Compare HTTP-01 and DNS-01 (wildcards need DNS-01).
Custom domains: verification by DNS TXT record, renewal handling.

Do not expose app containers directly to the internet.

---

# 14. Storage

- Images: registry, immutable tags plus digests.
- Artifacts and archived logs: object storage.
- PostgreSQL: metadata only. No large artifacts, no unbounded logs.
- Retention policies for artifacts, images, logs, previews. A garbage collector with dry-run mode.

---

# 15. Dashboard

API first. The dashboard grows with each milestone. Do not build all pages upfront.

MVP pages (by Milestone 4 to 6): authentication, organization and project list, create project and connect repo, project overview, build list and build details with live logs, deployment list and details with rollback, environment variables, runners.

Later: branches, preview list, domains, monitoring, platform admin.

Requirements: strict TypeScript, reusable components, dark and light theme, TanStack Query for server state, loading, error and empty states, error boundaries, live logs over SSE or WebSocket with resume, no mock data for completed features.

---

# 16. Security

Assume every connected repository contains hostile code.

Required:

- One-time runner registration tokens. Per-runner credentials. Job-scoped credentials: a runner receives only the secrets of the job it claimed, with a short-lived job token. A compromised runner must not read other projects' secrets.
- Webhook signature verification. API authentication. Role-based access control. Tenant isolation: one organization can never access another's builds, images, logs, artifacts or deployments (test it).
- Secret encryption at rest. Secret masking in logs (best effort, and document its limits).
- Build and runtime containers: no `--privileged`, drop capabilities, default seccomp profile, non-root user, no Docker socket inside any user container, CPU, memory, pids and time limits, log size limits, output size limits, disk limits (be honest about what Docker can and cannot enforce), container cleanup.
- Network: build and app containers cannot reach the control plane's internal network, the database, the registry's admin interfaces, other tenants' containers or the cloud metadata address (169.254.169.254). Decide the egress policy for builds in an ADR.
- Rootless Docker or BuildKit where practical. Evaluate it.
- Fork PR policy (section 8).
- Never execute repository code in the control plane process. Never expose the Docker daemon socket over any API.

Write `docs/threat-model.md` (STRIDE style) and keep it updated.

Document the limits of container isolation. Do not claim ordinary Docker containers fully protect against malicious tenants. For production-grade hostile workloads, evaluate gVisor, Kata Containers, Firecracker microVMs or dedicated VMs.

Run the hostile fixtures from section 10 as automated security tests.

---

# 17. Observability and Targets

Metrics: active, queued and failed builds, build duration, queue wait time, runner availability and heartbeat age, job claim latency, container count, CPU and memory, deployment health, API latency.

Structured logs everywhere. Correlation IDs on every event: `build_id`, `job_id`, `deployment_id`, `runner_id`, `request_id`.

Starting targets (adjust at Gate B, and prove with tests):

- Webhook acknowledged in under 500 ms (p95).
- Idle runner claims a new job in under 2 seconds.
- A job from a crashed runner is recovered within 2 x lease TTL.
- Zero failed requests during redeploy under load.
- 20 concurrent builds on one node with no double execution.

---

# 18. Repository Structure

Monorepo, independently deployable services:

```text
cloudforge/
├── control-plane/        (stack-specific layout, thin controllers, services and domain objects)
├── runner/
│   ├── cmd/
│   └── internal/{agent,api,executor,git,docker,strategies,logs,artifacts,heartbeat}/
├── frontend/
│   └── src/{components,pages,hooks,services}/
├── contracts/            (OpenAPI for the versioned runner API, /v1, plus contract tests)
├── fixtures/             (sample and hostile apps)
├── infrastructure/{docker,nginx,prometheus,grafana,scripts}/
├── docs/{architecture,api,adr,operations,threat-model.md,learning-log.md,stubs.md,progress.md}
├── CLAUDE.md             (conventions and how to resume work in a fresh session)
├── docker-compose.yml
├── Makefile              (make up, test, e2e, demo, chaos)
└── README.md
```

Improve it when there is a clear reason. Do not add abstraction layers without a reason.

---

# 19. Milestones (vertical slices)

Each milestone has: goal, scope, YOU BUILD IT, INTERNALS LAB, BREAK IT, and a runnable acceptance check.

## M0: Design gates (no application code)

Gates A, B, C from section 21. Internals lab: run a container by hand with `unshare` and cgroup limits.
Deliverable: approved architecture, ADRs, ER diagram, state machines, protocol, repo skeleton plan.

## M1: Walking skeleton

Scope: Docker Compose with PostgreSQL, control plane (health endpoint, create project, trigger build via API or CLI), one Go runner, registry, Nginx. Trigger a build for a fixture Dockerfile app. Runner claims a job, clones, builds with BuildKit, pushes, deploy agent starts the container, Nginx routes, URL works in the browser.
No auth, no UI, no webhooks yet.
YOU BUILD IT: the runner job-claim loop.
Internals lab: build an image with BuildKit by hand and read its cache behavior.
Break it: kill the runner mid-build and see what the system does today (it is allowed to be bad).
Deliverable: `make demo` goes from nothing to a working URL.

## M2: Reliable job system

Scope: leases, heartbeats, fencing tokens, reaper, retries, cancellation, capacity, priorities, graceful shutdown, explicit Build and Job state machines.
YOU BUILD IT: the state machine and the lease and fencing logic.
Internals lab: reproduce a double-claim race with two `psql` sessions, then fix it with `SKIP LOCKED`.
Break it: all 10 failure scenarios in section 9, automated in `make chaos`.
Deliverable: failure tests pass.

## M3: Git providers and webhooks

Scope: provider interface, Gitea and GitHub implementations, signature verification, delivery storage, idempotency, push and PR events, commit statuses, auto-cancel of superseded builds.
YOU BUILD IT: the idempotency and supersede logic.
Break it: replay the same webhook 50 times, send events out of order.
Deliverable: push to Gitea creates exactly one build; GitHub works through a tunnel.

## M4: Identity, projects and minimal dashboard

Scope: authentication, organizations, RBAC, projects, encrypted env vars, job-scoped credentials, MVP dashboard pages with live logs.
YOU BUILD IT: the log chunk ingestion with sequence numbers and resume.
Break it: disconnect the browser mid-stream and resume without losing lines.
Deliverable: tenant isolation tests pass.

## M5: Build engine and strategies

Scope: detection engine, Node, Next.js (SSR and static), Python and Dockerfile strategies, test step, BuildKit cache, per-step status.
YOU BUILD IT: the detection engine and one strategy (your choice).
Break it: ambiguous repo, broken build, huge log.
Deliverable: all fixtures build and run.

## M6: Deployment engine

Scope: deploy agent reconciler, deployment state machine, blue-green with health checks, rollback, restarts, resource limits.
YOU BUILD IT: the reconciliation loop.
Break it: a bad release that fails health checks, agent crash mid-deploy.
Deliverable: zero-failed-request redeploy test passes; rollback works.

## M7: Previews and Runbot features

Scope: PR previews, cleanup and TTL, preview env var scope, per-preview database from a template, branch type policies, fork PR policy.
YOU BUILD IT: the preview garbage collector.
Break it: close and reopen a PR rapidly, open a fork PR.
Deliverable: every eligible PR gets an isolated preview that is removed on close.

## M8: Production hardening

Scope: Prometheus and Grafana, local TLS, Let's Encrypt (if a real domain exists), custom domains, backups and restore, retention, quotas, the full security test suite, load tests. Dogfood: CloudForge builds and deploys itself.
Deliverable: usable self-hosted platform, threat model reviewed.

## M9: Distributed and stretch

Scope: multiple nodes, runner autoscaling design, Kubernetes executor design, stronger isolation evaluation. Implement multi-node runners. The rest are design documents with a prototype of one.
Deliverable: workloads run across at least two machines or VMs.

---

# 20. Engineering Standards

Backend: thin controllers, logic in services and domain objects, transactions for state changes, migrations, validation, unit and integration tests, structured logs, documented API.
Frontend: strict TypeScript, reusable components, API abstraction layer, minimal global state, tests for critical interactions.
Go runner: context cancellation everywhere, timeouts, graceful shutdown, explicit interfaces, structured logs, few dependencies, unit and integration tests.
Infrastructure: versioned config, env vars for settings, no committed secrets, reproducible through Compose, health checks, operations docs.
Testing: unit tests for pure logic (state machines, detection, planning), integration tests with real PostgreSQL and Docker, `make e2e` for the full workflow, `make chaos` for failure tests, contract tests for the runner API.

---

# 21. How You Must Work With Me

## Evidence labels

Label every claim about a real system:

- [SOURCE]: read in actual source code (give repo and file).
- [DOCS]: stated in official documentation (give the link).
- [INFERRED]: a pattern inferred from public information.
- [OUR DECISION]: our own design choice.

Do not invent internal details of commercial platforms such as Vercel and Render. If you cannot access source code or documentation in this session, say so clearly instead of guessing.

## Gates

- Gate A, Research: Runbot source, GitLab Runner source and CI architecture docs, BuildKit and Docker docs, Nginx docs, Vercel and Render public docs.
- Gate B, Architecture: diagrams, component responsibilities, protocols, ER diagram, state machines, ADRs for the stack and the key decisions in 5.2, security architecture, failure scenarios, targets.
- Gate C, Technical design: API contracts, schema, repo skeleton, local setup.

Each gate is one response (or a few). I approve each gate before the next. Do not combine gates.

## Per-milestone loop

1. Explain what we build and why, with design options and trade-offs. Wait for my "go".
2. INTERNALS LAB.
3. Build in small, commit-sized, verified steps (tests first when sensible). One step per response, no thousand-line dumps.
4. Run it. Paste real output. Never claim something works without running it.
5. BREAK IT.
6. Review: bugs, security, scalability, technical debt, test coverage. Update docs and the threat model.
7. EXPLAIN BACK and learning log.

## Session continuity

Keep `docs/progress.md` (current milestone, decisions made, open questions, next step) and `CLAUDE.md` (conventions, commands, how to resume) up to date after every step, so a fresh session can continue without me re-explaining.

## Rules

- Do not silently change the agreed stack. If you think it should change, explain and wait for approval.
- Push back when my request is a bad idea.
- List every stub or simplification in `docs/stubs.md`.
- Keep answers concise, in plain language. Skip definitions I already know.

---

# 22. What I Should Be Able to Explain Without Notes

- How a lease plus a fencing token prevents double execution, and what it still cannot prevent.
- What Docker actually does with namespaces and cgroups, and where its isolation stops.
- How BuildKit caches layers and why the build model uses one execution path.
- Why a deployment waits for health checks before routing switches, and how rollback reuses the deploy path.
- Why pull-based runners and desired-state reconciliation are used.
- How wildcard routing, local DNS, TLS and DNS-01 vs HTTP-01 work.
- How webhook idempotency works.
- Why `.local` is the wrong local domain suffix.
- How a fork PR can steal secrets, and how we prevent it.
- What we simplified compared with Runbot, GitLab and Vercel, and what would break first at 100x scale.

---

# 23. Final Acceptance

Everything below must work end-to-end with real implementations, not simulated UI:

1. A developer registers, creates an organization and a project, connects a repository and selects a branch.
2. A push triggers a webhook and exactly one build.
3. A runner claims the job, clones the exact commit, detects the app, runs the correct strategy, runs tests, builds and pushes the image.
4. CloudForge creates a deployment. The container starts, health checks pass, routing switches, and the generated URL serves the app.
5. The dashboard shows live build logs, step details, and deployment information.
6. A new commit redeploys with zero failed requests under load.
7. Rollback to a previous successful deployment works.
8. A PR creates an independent preview with its own URL and database, and cleanup happens on close.
9. A fork PR gets no secrets and no preview until approved.
10. `make chaos` passes (all failure scenarios in section 9).
11. The hostile fixtures cannot escape limits, read the Docker socket, reach the metadata address or other tenants.
12. Tenant isolation tests pass.
13. The targets in section 17 are met or the gaps are documented.

The goal is a working, self-hosted platform that teaches me how deployment platforms are engineered internally. It is not to compete with commercial products.

---

# 24. First Response Instructions

Do not write application code yet.

Do Gate A only:

1. Research findings from the Odoo Runbot source and the GitLab Runner source (with evidence labels): how builds are triggered, how jobs are assigned, how runners authenticate and poll, how cancellation and failure recovery work.
2. Short notes on BuildKit, Nginx dynamic routing, and the public Vercel and Render docs, with evidence labels.
3. What you could not verify.

Then ask me, in one batch, the decisions you need before Gate B:

- Stack profile (A or B), confirmed.
- My host OS and Docker setup (Linux, macOS, WSL2). It changes cgroup labs and isolation options.
- GitHub App vs OAuth App preference, and whether I have a public tunnel option.
- How many machines or VMs I can use for Milestone 9.
- How much time per week I can spend (to size the milestones).

Stop after that and wait for my answers.
