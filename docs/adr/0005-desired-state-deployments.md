# ADR-0005: Desired-state deployments and node-local routing

Status: accepted 2026-11-05 (Gate B part 1). Amended 2026-11-06 by part 3: containers join their environment's network; the proxy is Nginx (ADR-0010).
The reconciliation loop is YOU BUILD IT in M6.

## Context

Spec 5.1: the control plane never talks to Docker. Spec section 11: blue-green with health checks; rollback is a new deployment of an older digest. Spec 5.2.8: Nginx reaches apps by container name on a shared network (replaced by one network per environment, ADR-0010).

## Evidence

- `[DOCS]` Kubernetes controllers are control loops that move the current state toward the desired state (thermostat analogy) (https://kubernetes.io/docs/concepts/architecture/controller/).
- `[SOURCE]` Each Runbot builder loop rebuilds its view from the database, marker files and Docker on every turn, and renders its own Nginx config from its running builds, then reloads with `SIGHUP` (`runbot/models/runbot.py:46-79, 198-235`).
- `[DOCS]` Render: the new instance must pass health checks before traffic switches; the old instance gets `SIGTERM` 60 s later; a failed deploy leaves the old instance serving (https://render.com/docs/deploys).
- `[DOCS]` Docker: whoever controls the daemon controls the host (https://docs.docker.com/engine/security/).

## Options

1. The control plane drives Docker remotely (Docker API over TCP).
2. The control plane sends imperative commands to the agent ("start X", "stop Y").
3. The agent polls desired state and reconciles.

## Decision

Option 3. The control plane owns the deployment state machine and writes desired state. The agent only makes reality match and reports what it sees.

## How a deploy flows

1. A build succeeds. The control plane creates deployment D2 and adds it to the node's desired state as running but not routed. The desired state carries a generation number.
2. The agent sees a new generation, pulls D2's image by digest, starts the container with a `cloudforge.deployment=D2` label on its environment's network (`cf-env-<id>`, ADR-0010), runs health checks, and reports D2 healthy along with the generation it observed.
3. The control plane marks D2 healthy, sets the app's route to D2, and marks D1 draining.
4. The agent renders the proxy config from the routes, tests it, reloads, and reports the route generation as applied.
5. After a grace period, the control plane sets D1 to stopped and the agent stops it. D1's image is kept for rollback.

Failure cases:
- Health checks fail: D2 goes to failed, the route never moves, D1 is untouched.
- The agent crashes mid-deploy: on restart it lists containers by label and continues from where reality is.
- Rollback is deployment D3 with D1's digest, through the same flow.

## Why

- Option 1 puts root-equivalent Docker access in the control plane and on the network.
- Option 2 loses commands when the agent restarts or the network drops, and no single place says what should be running.
- Option 3 heals itself, survives restarts, and every step is visible in the database.

## Consequences

- A deploy takes a few agent poll intervals (2 s each) longer than push would. Acceptable.
- The agent owns its node's Nginx config: a generated file plus reload. The proxy is Nginx (ADR-0010).
- The agent credential is separate from the runner credential, because desired state contains runtime secrets.
- Multi-node (M9): builds can run on any node; apps stay on the edge node until a multi-node routing design exists (M9 design doc).
- M1 shortcut: desired state = "latest successful build per project", stop old then start new, no blue-green. Listed in `docs/stubs.md` when M1 starts.
