# ADR-0002: Runner transport: pull over HTTPS, short poll first

Status: accepted 2026-11-05 (Gate B part 1).

## Context

Runners and agents must get work without the control plane connecting to them (spec 5.2.1). Target: an idle runner claims a new job in under 2 s (spec 17).

## Evidence

- `[SOURCE]` GitLab Runner polls `POST /api/v4/jobs/request` every `check_interval` (default 3 s), spread across its configured runners; `204` means nothing to do (`gitlab-runner commands/multi.go:1120-1137`, `network/gitlab.go:651-733`).
- `[SOURCE]` GitLab's long poll lives in Workhorse, which watches a Redis key per runner; Rails also answers `204` from a cached value without scanning the queue (`workhorse/internal/builds/register.go:121-187`, `lib/api/ci/helpers/job_request.rb:42-73`). This machinery exists for very large runner fleets.
- `[SOURCE]` Runbot builders have no HTTP API: they read and write the database directly (`runbot_builder/tools.py:158-210`).

## Options

1. Short poll: `POST /v1/jobs/claim` every 1 s with jitter, only while the runner has a free slot.
2. Long poll: the server holds the request up to about 25 s and answers when a job is queued (wake-up via PostgreSQL LISTEN/NOTIFY).
3. Persistent connection (WebSocket or gRPC stream) with server push.
4. Push: the control plane calls the runners.
5. Direct database access, as Runbot does.

## Decision

Option 1 now. Option 2 is the documented upgrade path.

## Why

- With 5 runners or fewer, one request per second per idle runner costs nothing, and latency of at most 1 s meets the 2 s target.
- Long polling adds async database listeners and timeout handling for no measurable gain at this scale.
- Push needs inbound connectivity to runners (NAT, firewalls) and makes the control plane track runner addresses.
- Direct database access gives every build node the full database credential: one compromised node means the whole platform. Rejected.
- Persistent streams need reconnect logic, suffer proxy idle timeouts, and can't be debugged with `curl`.

## Consequences

- The agent uses the same pattern: it fetches desired state every 2 s.
- Runner and agent use separate credentials (see ADR-0005).
- Revisit when runners exceed about 50, or when claim queries show up in database metrics.
