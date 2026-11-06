# Stubs and simplifications

Every shortcut in the code, and when it goes away. Remove a row when its replacement lands.

| Since | Shortcut | Replaced in | Why it is fine for now |
|---|---|---|---|
| M1 slice 1 | `/healthz` checks only the database | M8 (production hardening) | the database is the only dependency so far |
| M1 slice 1 | Python packages are pinned, their own dependencies are not | when an upgrade breaks a build: add a lock file | few packages, each pinned exactly |
| M1 slice 1 | the test database `cloudforge_test` lives in the dev PostgreSQL | M8, when CloudForge builds and tests itself | tests drop only what the owner role created |
