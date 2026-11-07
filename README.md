# CloudForge

A learning-first, self-hosted CI/CD and deployment platform: push a commit, get a running app at a URL.
It is built one milestone at a time. The contract is `docs/spec.md`.

## Start here

1. `docs/codebase-map.md`: what is built, how it runs, and where to look (20 min).
2. `docs/progress.md`: where the project stands, and the next step.
3. `docs/architecture/overview.md`: the full design, including the parts not built yet.

## Run it

Needs Linux, Docker with Compose, GNU make, Go 1.26 and `htpasswd`. Details: `docs/operations/local-setup.md`.

- `make up`: build and start the stack, then open `http://cloudforge-console.localhost:8080/healthz`.
- `make test`: Go, Python, and the BuildKit test.
- `make down`: stop the stack. Its data stays in Docker volumes.
- `postman/cloudforge.postman_collection.json`: try the API by hand. Every request works on its own.
