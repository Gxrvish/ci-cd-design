# Stubs and simplifications

Every shortcut in the code, and when it goes away. Remove a row when its replacement lands.

| Since | Shortcut | Replaced in | Why it is fine for now |
|---|---|---|---|
| M1 slice 1 | `/healthz` checks only the database | M8 (production hardening) | the database is the only dependency so far |
| M1 slice 1 | Python packages are pinned, their own dependencies are not | when an upgrade breaks a build: add a lock file | few packages, each pinned exactly |
| M1 slice 1 | the test database `cloudforge_test` lives in the dev PostgreSQL | M8, when CloudForge builds and tests itself | tests drop only what the owner role created |
| M1 slice 2a | no login: every dashboard call acts for the seeded organization `default` | M4 (ADR-0015) | the API only listens on this machine |
| M1 slice 2a | a repository's `external_id` comes from the request, unchecked | M3, which checks it with the provider | nothing routes by it before webhooks |
| M1 slice 2a | state changes are plain guarded `UPDATE`s with no event rows | M2: the state machine, YOU BUILD IT | M1 is allowed to be bad (spec M1) |
| M1 slice 2a | manual builds always target the production branch | M7, with the branch types (ADR-0013) | only production environments exist |
