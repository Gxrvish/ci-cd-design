# CloudForge

Learning-first, self-hosted CI/CD and deployment platform. The full contract is in `docs/spec.md`. Read it before doing anything.

## Resume in a fresh session

1. Read `docs/progress.md` (current milestone, open questions, next step).
2. Read the latest gate or milestone doc it points to.
3. Continue from "Next step". Do not redo approved gates.

## Working rules (summary of docs/spec.md, section 0 and 21)

- Gates A, B and C are approved (2026-11-05 and 2026-11-06). From M1 on, follow the per-milestone loop (spec 21): explain the plan and wait for "go", then the lab, then build in small verified steps, then break it, review, and explain back.
- YOU BUILD IT pieces: give the interface, acceptance tests and hints only, until the user says SHOW ME.
- Modes: BUILD (default), HINT MODE (hints only, one at a time), REVIEW MODE (strict review, then the single most important fix).
- Label claims about real systems: [SOURCE] (repo + file:line), [DOCS] (link), [INFERRED], [OUR DECISION], [RAN] (command run in session).
- Never invent internals of Vercel or Render.
- Every milestone: internals lab, break it, 5 explain-back questions (answers hidden), real-world note, append to `docs/learning-log.md`.
- Labs: about 15 min, plain commands. Each step names the CloudForge decision it explains.
  - I run them and show the real output, with what each step means. The user doesn't type the commands.
  - Reasons (user, 2026-11-06): the first M0 sheet (45 min, two terminals) felt awkward, and later: "Not into copy pasting commands."
- List every stub or simplification in `docs/stubs.md`.
- Update `docs/progress.md` and this file after every step.
- Code: clean code, SOLID, DRY, KISS and YAGNI, strictly (user rule, 2026-11-06).
- SQL: psycopg 3 with bound parameters only, never values formatted into SQL text. No ORM (ADR-0014).
- Postman: when an endpoint changes, update `postman/` in the same delivery (user, 2026-11-06).
- Git: branch `main`, created 2026-11-06. Commit each delivery once it is verified. The user reviews afterwards with `git show` (user, 2026-11-06: "commit always"). Never push.
  - Never add a `Co-Authored-By` line for Claude to a commit (user, 2026-11-06).
- Every change: aim for at most 500 added or changed lines per delivery, code and docs together, and report the count.
  - Go over only when splitting would make it harder to understand, and say why.
  - Reason (user, 2026-11-06): big diffs are tiring to read and hard to follow.
- The user is an Odoo backend developer: skip HTTP, SQL and Python basics; explain Go, container internals, BuildKit, scheduling, proxies and TLS concisely.

## Layout

- `docs/spec.md`: the contract (verbatim).
- `docs/progress.md`: state and decisions.
- `docs/research/`: gate research with evidence.
- `docs/architecture/`: overview (diagrams, components, protocol), data model (ER), state machines, failure scenarios and targets.
  - `schema-check.sql`: checks that the rules of `control-plane/migrations/0001_initial.sql` (the full DDL) hold.
- `docs/adr/`: one file per decision, `NNNN-title.md` (Status, Context, Evidence, Options, Decision, Why, Consequences).
- `contracts/v1.yaml`: OpenAPI 3.1 for the node API `/v1` (runner and agent).
- `docs/labs/`: one internals lab per milestone. I run it and show you the output.
- `docs/learning-log.md`: one entry per milestone: what the lab showed, the explain-back result, a real-world note.
- `docs/operations/local-setup.md`: repository layout, Compose services and networks, secrets, make targets.
- `cfnode/`: the Go binary for runner and agent (`internal/api`, `internal/runner`).
- `docs/you-build-it/`: one task sheet per YOU BUILD IT piece: the rules, the Go or SQL needed, how the tests work.
- `control-plane/`: the Python API (`cloudforge/`), SQL migrations (`migrations/`) and tests (`tests/`).
- `infrastructure/`: PostgreSQL init SQL, Nginx config. `docker-compose.yml` and `Makefile`: the local stack.
- `docs/stubs.md`: every shortcut in the code and the milestone that replaces it.
- `postman/`: a Postman collection of the HTTP API. Any request works alone: a collection script first creates the demo data it needs.
- `docs/api/dashboard.md`: dashboard API plan (conventions, roles, endpoints per milestone). FastAPI generates the exact schema.
- `docs/threat-model.md`: STRIDE threats per trust boundary, residual risks, security tests by milestone.
- `docs/milestone-plan.md`: hours, weeks and dates per milestone at 6 h/week.

## Host facts

Ubuntu 22.04, kernel 6.8, cgroup v2, rootful Docker 29.9 (overlay2 on ext4), buildx default `docker` driver, Go 1.26, Python 3.12, Node 20. No nginx, mkcert or buildctl installed; run them as containers. Apache runs on port 80, so CloudForge uses 8080.

## Commands

- Local stack: `make up` (then `http://cloudforge-console.localhost:8080/healthz`), `make test` (Python and Go), `make down`.
- Go only: `make test-go` (host Go 1.26: gofmt, vet, tests with `-race`).
- Postman collection against the running stack (it creates rows):
  `docker run --rm --network host -v "$PWD/postman:/etc/newman:ro" postman/newman:6.1.3-alpine run cloudforge.postman_collection.json --color off`
- Schema check (throwaway PostgreSQL 18, no network, removed afterwards), from `docs/architecture/`:

  ```sh
  docker run -d --rm --name cf-schema-check --network none -e POSTGRES_PASSWORD=unused postgres:18.6
  # wait until `docker logs cf-schema-check` shows "PostgreSQL init process complete" and pg_isready succeeds
  docker exec -i cf-schema-check psql -U postgres -v ON_ERROR_STOP=1 -q -c "CREATE ROLE cloudforge_app LOGIN"
  docker exec -i cf-schema-check psql -U postgres -v ON_ERROR_STOP=1 -q -1 -f - < ../../control-plane/migrations/0001_initial.sql
  docker exec -i cf-schema-check psql -U cloudforge_app -d postgres -v ON_ERROR_STOP=1 -f - < schema-check.sql
  docker stop cf-schema-check
  ```

- Contract check: `python3 -m venv /tmp/oas && /tmp/oas/bin/pip install -q openapi-spec-validator && /tmp/oas/bin/openapi-spec-validator contracts/v1.yaml`
