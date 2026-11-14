# Stubs and simplifications

Every shortcut in the code, and when it goes away. Remove a row when its replacement lands.

| Since | Shortcut | Replaced in | Why it is fine for now |
|---|---|---|---|
| M1 slice 1 | `/healthz` checks only the database | M8 (production hardening) | the database is the only dependency so far |
| M1 slice 1 | Python packages are pinned, their own dependencies are not | when an upgrade breaks a build: add a lock file | few packages, each pinned exactly |
| M1 slice 1 | the test database `cloudforge_test` lives in the dev PostgreSQL | M8, when CloudForge builds and tests itself | tests drop only what the owner role created |
| M1 slice 2a | no login: every dashboard call acts for the organization `default`, a seed (`control-plane/seeds/`) | M4 (ADR-0015) | the API only listens on this machine |
| M1 slice 2a | a repository's `external_id` comes from the request, unchecked | M3, which checks it with the provider | nothing routes by it before webhooks |
| M1 slice 2a | state changes are plain guarded `UPDATE`s with no event rows | M2: the state machine, YOU BUILD IT | M1 is allowed to be bad (spec M1) |
| M1 slice 2a | manual builds always target the production branch | M7, with the branch types (ADR-0013) | only production environments exist |
| M1 slice 2b | claims take no `SKIP LOCKED`; leases are set but nothing checks them (no heartbeat, no reaper) | M2: lease and fencing, YOU BUILD IT; the M2 lab adds `SKIP LOCKED` | one runner, so nothing races |
| M1 slice 2b | a claim's `secrets` is always empty | M4, with project variables (ADR-0007) | no variables exist yet |
| M1 slice 2b | `clone_token` is always null: public Gitea repositories only | M3, with GitHub installation tokens (ADR-0011) | the fixture repository is public |
| M1 slice 2c | logs: each chunk is stored once; no gap check (416), no per-attempt limit, no SSE or resume; the log is read as plain text | M4: log ingestion, YOU BUILD IT (ADR-0006) | builds are short and watched by hand |
| M1 slice 2c | complete checks only the token, the attempt and `running`; no lease expiry, no event rows | M2: the state machine, lease and fencing, YOU BUILD IT | one runner, no retries yet |
| M1 slice 2c | no `/heartbeat`, `/steps` or `/release` yet | M2 (heartbeat, release) and M5 (steps) | nothing checks leases, and builds have no steps yet |
| M1 slice 3 | buildkitd's state lives on a Docker volume, not on a fixed-size filesystem, so a build can fill the host's disk | M5, before the disk-fill fixture runs (ADR-0009, threat 20) | only our own fixtures build until then |
| M1 slice 3c | the log upload masks no secrets (ADR-0006 rule 1) | M4, with project variables (ADR-0007) | claims carry no secrets before M4 |
| M1 slice 3c | the runner treats 416 (a gap in the log) as a refusal, and stops uploading the log | M4: log ingestion, YOU BUILD IT (ADR-0006 rule 3) | the M1 API never answers 416 |
| M1 slice 3c | a runner that stops in the middle of a build drops the job: it stays `running` for good, and keeps the runner's only slot | M2: release (J10), lease expiry and the reaper (ADR-0003) | M1 is allowed to be bad (spec M1); slice 5's break-it session shows it |
| M1 slice 3c | the runner ignores `cancel_requested` | M2, with `POST /api/builds/{id}/cancel` | nothing sets the flag before then |
| M1 slice 3c | a build has two steps, clone and build: a failed push counts as build, and nothing reports steps | M5, with the strategies (ADR-0008) | one strategy, the user's Dockerfile, and short builds |
| M1 slice 3c | the runner checks neither the pushed image's size (`max_bytes`) nor the size of a checkout | M5, with the build engine's limits (ADR-0012) | only our own fixtures build until then |
| M1 slice 3c | the runner runs one job at a time, is named after its machine, and has no labels | M2, when the lab needs several claims at once | one runner |
| M1 slice 3 | the registry has one shared `runner` account (htpasswd) that can push to and pull from every repository | M8: job-scoped registry tokens (ADR-0009) | one organization and one runner |
