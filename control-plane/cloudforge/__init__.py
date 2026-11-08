"""The CloudForge control plane: an HTTP API that keeps all state in PostgreSQL (docs/codebase-map.md).

It never runs repository code and never talks to Docker: runners and agents do that, and call this API.

Modules, in the order a request meets them:
- api: the FastAPI app. It opens the database pool and mounts the two routers.
- dashboard: /api, the calls people and scripts make (repositories, projects, builds, runner tokens).
- node_api: /v1, the calls runners make (register, claim, logs, complete), as in contracts/v1.yaml.
- db: connection settings, one pooled connection per request, and rows as dataclasses.
- credentials: random secrets that only the caller sees; the database keeps their hashes.
- migrate: applies migrations/*.sql in order. The migrate service runs it before the api starts.
- seed: applies seeds/*.sql, the data every installation needs, right after the migrations.
"""
