# ADR-0001: Control plane stack (Profile A)

Status: accepted 2026-11-05 (Gate B part 1). Amended 2026-11-06 by ADR-0014: psycopg 3 and plain SQL migrations replace SQLAlchemy and Alembic.

## Context

The control plane serves REST for the UI, the runner API, webhook intake and long-lived SSE log streams, and runs a few background loops. The user knows Python, PostgreSQL and the Odoo ORM, and is new to Go, containers and scheduling. The spec offers Profile A (FastAPI) or Profile B (Rails, Redis, Sidekiq). The user chose A on 2026-11-05.

## Options

1. Profile A: FastAPI, SQLAlchemy 2.0, Alembic, Pydantic v2. Background work = PostgreSQL tables + a worker process. No Redis.
2. Profile B: Rails modular monolith, Redis, Sidekiq.
3. Django, Django REST framework, Celery with a broker.

## Decision

Option 1.

## Why

- The learning budget goes to Go, containers and scheduling, not to a new control-plane language.
- Async request handling serves SSE streams (and a later long poll) without extra servers.
- PostgreSQL is the only stateful dependency: state, queue, events. Redis only after a measured need (spec section 4).
- Option 2 adds Ruby, Redis and Sidekiq: three new things that are not the learning target.
- Option 3: the Django ORM feels like Odoo's and would hide the transactions and row locks we want to see. Celery needs a broker.

## Consequences

- Transactions and row locks are written explicitly in SQL, through psycopg 3 (ADR-0014): more code than Rails, but visible.
- Background work is a few small loops, each over one table with a status column (webhook deliveries, expired leases, commit-status outbox), each claimed with row locks. No generic task framework until there are more than about five loops.
- The runner and agent are Go, as the spec decides: one static binary per node. BuildKit's own client is a Go package (`github.com/moby/buildkit/client` `[SOURCE]`), and GitLab Runner is Go `[SOURCE]`.
- Three languages in total: Python, Go, TypeScript. No more without approval.
