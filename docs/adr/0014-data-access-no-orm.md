# ADR-0014: Data access and migrations without an ORM

Status: accepted 2026-11-06 (Gate C). It changes the agreed stack (spec 4, ADR-0001): SQLAlchemy and Alembic are dropped. You approved it on 2026-11-06.

## Context

- The schema is hand-written SQL (`schema.sql`, accepted).
  - SQLAlchemy can describe most of it: composite keys, partial unique indexes, CHECKs, domains.
  - No ORM model holds the transition trigger, the grants or the transition rows. Those stay in hand-written migrations, whatever we choose.
- Every state change is an explicit compare-and-set or `SKIP LOCKED` query (ADR-0003, `state-machines.md`). Seeing those queries is part of the learning goal.
- 2026-11-06, you asked whether we use an ORM at all, and set the rule: DRY, KISS and YAGNI, strictly.

## Evidence

- `[DOCS]` SQLAlchemy is two APIs:
  - Core, the SQL expression language, which is "command oriented";
  - the ORM on top of it, which automates DML through a unit of work and is "state oriented".

  Source: https://docs.sqlalchemy.org/en/20/intro.html.
- `[DOCS]` Alembic's autogenerate doesn't detect triggers. Detecting CHECK constraints is off by default and compares names only (https://alembic.sqlalchemy.org/en/latest/autogenerate.html).
- `[DOCS]` psycopg 3 sends the query and its parameters to the server separately. Without parameters, one `execute()` may run several statements (https://www.psycopg.org/psycopg3/docs/basic/from_pg2.html).
- `[DOCS]` psycopg 3 row factories return rows as instances of a class, for example `class_row(Person)` (https://www.psycopg.org/psycopg3/docs/advanced/rows.html).
- `[DOCS]` Connection pools, sync and async, ship as the separate package `psycopg_pool` (https://www.psycopg.org/psycopg3/docs/advanced/pool.html).
- `[DOCS]` SQLAlchemy supports the PostgreSQL features we use:
  - `with_for_update(skip_locked=True)` (https://docs.sqlalchemy.org/en/20/core/selectable.html);
  - `on_conflict_do_nothing()`, partial indexes (`postgresql_where`) and a `DOMAIN` type (https://docs.sqlalchemy.org/en/20/dialects/postgresql.html).
- `[DOCS]` SQLAlchemy ORM behavior that matters to us:
  - A flush updates rows by primary key. Only the optional `version_id_col` adds a version check, which raises `StaleDataError` (https://docs.sqlalchemy.org/en/20/orm/versioning.html).
  - Autoflush runs before queries (https://docs.sqlalchemy.org/en/20/orm/session_basics.html).
  - Objects are loaded once per session: a later matching row "is not applied to the object" unless you ask for `populate_existing` (https://docs.sqlalchemy.org/en/20/orm/queryguide/api.html).
  - With `AsyncSession`, lazy loads and expired attributes must be avoided, and one session is not safe across concurrent tasks (https://docs.sqlalchemy.org/en/20/orm/extensions/asyncio.html).
- `[SOURCE]` Odoo-based schedulers drop to SQL for claiming:
  - Odoo claims cron jobs with raw `FOR NO KEY UPDATE SKIP LOCKED` (odoo `odoo/addons/base/models/ir_cron.py:326-385`, LGPL-3, local checkout `5268d45f`).
  - Runbot's merge bot flushes pending ORM writes by hand before its own `FOR UPDATE SKIP LOCKED` (runbot `runbot_merge/models/pull_requests.py:1594-1604`, LGPL-3, `67892b2`).

## Options

1. SQLAlchemy ORM with Alembic autogenerate: the models define the tables.
2. SQLAlchemy Core with Alembic: SQL migrations and `text()` queries, no ORM layer. This keeps the agreed stack.
3. psycopg 3 and `psycopg_pool` only: plain SQL migration files, applied by a small runner.

## Decision

Option 3.

- **Queries:** SQL strings with bound parameters (`%(name)s`), never formatted into the text. Rows come back as dataclasses (`class_row`). Pydantic is used only at the HTTP edge, where FastAPI needs it.
- **Migrations:** files `control-plane/migrations/NNNN_name.sql`, forward only. `schema.sql` becomes `0001_initial.sql`. The runner (about 40 lines, M1):
  1. takes a session advisory lock, so two processes never migrate at once;
  2. creates `migration.schema_migration (version, sha256, applied_at)` if it is missing. It has a schema of its own, so the app role's grant on every table in `public` (0001) never covers it (amended in M1 slice 1);
  3. stops if an applied file's checksum has changed, because applied files are immutable;
  4. applies each pending file and records it, in one transaction per file.

## Why

Capability is not the reason: SQLAlchemy can express almost every query and constraint we need. The reasons are cost.

- **DRY:** each table is defined once, in SQL. Option 1 adds a second definition (the models), and so does option 2 as soon as it uses Core table objects.
- **YAGNI:**
  - The ORM's value (unit of work, relationship loading, identity map) automates exactly the SQL this project is meant to make visible.
  - Alembic's value (autogenerate, branches, downgrade scripts) would go unused.
- **KISS:** one driver family and two packages, with no configuration files. Odoo uses the same family (psycopg2), so the `execute()` style is familiar.
- **Learning:** the runner is a small lab of its own: version table, advisory lock, transactional DDL.

## Consequences

- **Agreed stack:** this replaces "SQLAlchemy 2.0, Alembic" in ADR-0001, which is amended. `spec.md` stays verbatim, and this ADR records the change.
- **What you won't learn here:** SQLAlchemy and Alembic, both common in Python teams. Option 2 keeps them, with the same SQL-first schema.
- **Tests:** hand-written SQL has no type checking, so every query runs against real PostgreSQL in the integration tests (spec 20 asks for those anyway).
- **Review rule:** SQL text never contains formatted values. The only exception is identifiers built with `psycopg.sql`.
- **No downgrades:** a bad migration is fixed by a new one.
