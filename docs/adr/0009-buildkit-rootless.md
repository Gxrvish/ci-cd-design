# ADR-0009: BuildKit deployment: one rootless buildkitd per runner

Status: accepted 2026-11-06 (Gate B part 3). Amended 2026-11-06 by the M1 lab: buildkitd runs as uid 61000.

## Context

- Spec 16 sets these rules for build containers:
  - no `--privileged`, capabilities dropped, the default seccomp profile, a non-root user, no Docker socket;
  - CPU, memory, pids, time and disk limits;
  - "Rootless Docker or BuildKit where practical. Evaluate it."
- Overview (accepted): the runner talks only to buildkitd, and buildkitd is never reachable from app containers.
- Gate A: this host's default `docker` buildx driver cannot export a registry cache.
- ADR-0007: runners are scoped to an organization for jobs with secrets.

## Evidence

- `[SOURCE]` A rootful buildkitd container is started with `--privileged` (buildkit `README.md:780`, Apache-2.0).
- `[SOURCE]` Rootless buildkitd in a container needs three Docker options: `seccomp=unconfined`, `apparmor=unconfined` and `systempaths=unconfined`. About the last one: it "potentially allows reading and writing dangerous kernel files from a container, but it is safe when you are running buildkitd as non-root" (`docs/rootless.md:68-95`).
- `[SOURCE]` The alternative to `systempaths=unconfined` is `--oci-worker-no-process-sandbox`, which "allows build containers to kill (and potentially ptrace) an arbitrary process in the BuildKit host namespace" (`executor/oci/spec.go:37-38`).
- `[SOURCE]` In rootless mode, "Network mode is always set to network.host": RUN steps share buildkitd's network namespace (`docs/rootless.md:9`).
- `[SOURCE]` BuildKit applies the default seccomp profile to every RUN container when seccomp is available (`executor/oci/spec_linux.go:78-80`). This doesn't depend on the profile of the buildkitd container itself.
- `[SOURCE]` Cache mounts are identified by an `id`, which defaults to the target path. With the default `sharing=shared`, several builds write to the same mount at once (`frontend/dockerfile/docs/reference.md:908-911`). Any build on the same buildkitd that names the same id gets the same directory.
- `[SOURCE]` Per-worker garbage collection limits: `reservedSpace`, `maxUsedSpace` and `minFreeSpace` (`docs/buildkitd.toml.md:109-123`).
- `[RAN]` Host prerequisites for rootless mode:
  - `kernel.apparmor_restrict_unprivileged_userns = 0`;
  - `kernel.unprivileged_userns_clone = 1`;
  - `/etc/subuid` has `odoo:100000:65536`.
- `[RAN]` (Gate A) The Docker root is on ext4, so `--storage-opt size` cannot cap a container's disk.

## Options

1. The buildx `docker` driver (the BuildKit built into dockerd).
2. The buildx `docker-container` driver (buildx creates and drives a BuildKit container).
3. A standalone rootful buildkitd container.
4. A standalone rootless buildkitd container (`moby/buildkit:rootless`).
5. Rootless buildkitd as a host service (RootlessKit installed on the host).

## Decision

Option 4, with one buildkitd per runner.

- **Who starts it:** Compose (later `make`, per node), never the runner.
  - Flags: the three `--security-opt` values above.
  - Never `--privileged`, never `--oci-worker-no-process-sandbox`.
- **Image:** a derived image (`infrastructure/buildkit/`) runs buildkitd as uid 61000.
  - The stock image runs as uid 1000, which is usually the first login account on a Linux desktop (`odoo` on this host).
  - An escape would then land in that person's files and shell `[RAN]`, M1 lab.
- **Connection:** the runner reaches buildkitd through a Unix socket on a volume shared only with its own buildkitd. There is no TCP listener, so app containers cannot reach it.
- **Limits:** `--cpus`, `--memory` and `--pids-limit` are set on the buildkitd container. That runner's concurrent builds share them.
- **Disk:**
  - buildkitd's state directory lives on a fixed-size filesystem: an image file, for example 20 GiB, that `make init` loop-mounts with sudo.
  - Garbage collection limits apply inside it.
  - A build that fills the disk gets "no space left" inside that filesystem, not on the host.
- **Network:** buildkitd containers sit on their own Docker network. Egress rules are in ADR-0012.

### A build cache is never shared across organizations or trust levels

Cache mounts are shared by id. On a shared buildkitd, a hostile Dockerfile can write into another tenant's cache mount, for example their npm cache, and that tenant's next build installs from it. This attacks the integrity of their image, and it needs no secrets at all.

So:

- **One organization per runner.** Every runner belongs to exactly one organization.
  - **This amends ADR-0007:** shared runners (no organization) are dropped.
  - In `data-model.md`, `runner.organization_id` becomes `NOT NULL`.
- **One pool per runner.** Every runner is in the `trusted` or the `untrusted` pool, recorded in a new column `runner.pool`.
  - Fork PR builds run only on `untrusted` runners.
  - `untrusted` runners never get trusted jobs (ADR-0013).
- **Remaining risk inside one organization and pool:** projects still share a buildkitd. A hostile repository with its own Dockerfile could target another project's cache mount id.
  - Recorded in the threat model.
  - The fix, one buildkitd per project, costs memory and is not planned.

### Registry access

- **M1:** the registry (`registry:2`) uses htpasswd basic auth, with one account for runners and one for agents.
  - Basic auth has no per-repository permissions `[INFERRED]`, so a stolen runner credential can push to and pull from any repository.
  - This goes into `docs/stubs.md`.
- **M8:** token auth. Each claim response carries a short-lived token scoped to `repository:<org>/<project>:push,pull`. The runner then holds no long-lived registry credential (spec 16, job-scoped credentials).
- **Credential path:** the runner hands registry credentials to the exporter through the BuildKit session, not to RUN steps `[INFERRED]`. A RUN step can reach the registry over the network but has no credential for it.

## Why

- **Options 1 and 2** put the Docker socket in the runner, because buildx manages builders through dockerd. That breaks overview rule 3 and places root-equivalent access next to hostile builds. Option 1 can't export a registry cache on this host either.
- **Option 3** needs `--privileged`, which spec 16 forbids.
- **Option 4** maps root inside a RUN step to the unprivileged buildkitd user on the host, through user namespaces. Escaping the RUN sandbox lands in an unprivileged process inside a container. That container has no Docker seccomp or AppArmor profile, which is the price of running rootless inside Docker. Going further needs a kernel bug.
- **Option 5** gives the same isolation without the unconfined Docker options, but it needs RootlessKit and a systemd user service on every host. Containers keep setup at `docker compose up`. Revisit if Docker's options become a problem.
- **Per runner, not per node** as the overview said: the cache rule above needs it. The cost is one idle buildkitd per runner.

## Consequences

- **Amends (applied 2026-11-06):**
  - overview: buildkitd becomes "container per runner";
  - ADR-0007: no shared runners;
  - `data-model.md`: `runner.organization_id NOT NULL`, new `runner.pool`.
- **`make init` gains a sudo step** for the loop-mounted filesystem. After a reboot it must be mounted again (fstab entry or make target).
- **No per-build limits inside one buildkitd:** a fork bomb in one build can starve the other builds of the same runner, but not the host. The M5 break-it session runs the fork bomb fixture to check this.
- **Run on this host** in the M1 internals lab `[RAN]` 2026-11-06. The results are in `docs/operations/local-setup.md`.
