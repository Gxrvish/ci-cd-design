# ADR-0012: Isolation limits and egress policy

Status: accepted 2026-11-06 (Gate B part 3).

## Context

Spec 16 assumes every repository is hostile. It requires:

- **For build and app containers:**
  - no `--privileged`, capabilities dropped, the default seccomp profile, a non-root user, no Docker socket;
  - CPU, memory, pids, time, log, output and disk limits, "be honest about what Docker can and cannot enforce";
  - container cleanup.
- **On the network,** build and app containers must not reach:
  - the control plane's network;
  - the database;
  - the registry's admin interfaces;
  - other tenants' containers;
  - `169.254.169.254`.

  The build egress policy is decided in an ADR.
- **Documented isolation limits,** plus an evaluation of gVisor, Kata, Firecracker or VMs.

Spec 12: a preview never modifies production, reads its secrets or routes traffic as production.

## Evidence

- ADR-0009: rootless BuildKit facts. ADR-0010: per-environment networks.
- `[DOCS]` Docker's FORWARD rules jump to `DOCKER-USER` first. Rules a user appends to FORWARD run after Docker's rules, so filtering of container traffic goes into `DOCKER-USER`. Packets there are already DNAT-ed, and conntrack matches the original address (https://docs.docker.com/engine/network/firewall-iptables/).
- `[DOCS]` Anyone who controls the Docker daemon is root-equivalent (Gate A, section 4).
- `[RAN]` `docker info` on this host:
  - security options `apparmor` (default profile) and `seccomp` (builtin profile);
  - firewall backend `iptables`;
  - Docker root on ext4, so no per-container disk quota (Gate A).
- `[SOURCE]` The stronger options, each from its README (Apache-2.0):
  - **gVisor** is "an application kernel that implements a Linux-like interface", written in a memory-safe language, as a layer between applications and the host (google/gvisor `README.md`).
  - **Kata Containers** are lightweight VMs that feel like containers but give a VM's workload isolation (kata-containers `README.md`).
  - **Firecracker** runs microVMs on KVM for secure multi-tenant workloads with little overhead (firecracker `README.md`).
- `[RAN]` (Gate A) KVM is usable on this host.

## Decision

### Builds

| Control | How | Enforced by |
|---|---|---|
| no `--privileged`, no Docker socket | rootless buildkitd; no Docker socket anywhere on the build path | ADR-0009 |
| seccomp | BuildKit's default profile on every RUN | BuildKit |
| non-root | root in a RUN step is an unprivileged host user | user namespaces |
| CPU, memory, pids | set on the buildkitd container; that runner's builds share them | cgroups |
| time | the runner's build timeout, with J9 as the backstop | ADR-0003 |
| log size | 10 MiB per attempt | ADR-0006 |
| disk | fixed-size filesystem for buildkitd state, plus garbage collection | ADR-0009 |
| output size | image size is checked after push; over the project limit (default 2 GiB) fails the build | runner, API |
| trust | fork builds only on the `untrusted` pool | ADR-0013 |

### Apps (containers the agent runs)

- **Security flags:** `--cap-drop ALL`, `--security-opt no-new-privileges`, and Docker's default seccomp and AppArmor profiles.
- **Never:** `--privileged`, a Docker socket, or a host path mount.
- **User:**
  - The image's `USER`, if it isn't root. Otherwise the agent forces uid:gid `10001:10001`.
  - Generated Dockerfiles always set a non-root user.
  - An app that only works as root fails to start, and the dashboard says why.
- **Filesystem:**
  - The root filesystem is read-only.
  - A size-limited `tmpfs` covers `/tmp` and any writable path the strategy declares (for example Next.js `.next/cache`).
  - That is the disk limit for apps on ext4. `[INFERRED]` tmpfs pages count toward the container's memory limit.
- **Limits:** CPU, memory and pids come from the environment's branch-type policy (ADR-0013). The Docker log driver gets a size cap.
- **Cleanup:** the agent removes every container that is no longer in its desired state.

### Network

| From | Allowed | Blocked |
|---|---|---|
| build steps (buildkitd's network) | the internet, because installs need it; the registry, which requires auth | see the blocked list below |
| app containers | the internet; its own environment network (Nginx, its own preview database) | the same blocked list, plus other environment networks |

Blocked for both:

- private ranges: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `100.64.0.0/10`;
- link-local `169.254.0.0/16`, which includes the metadata address;
- the host itself;
- every other CloudForge network.

How it's enforced:

- `make init` installs host iptables rules for each CloudForge subnet range:
  - in `DOCKER-USER`, for traffic forwarded through the host;
  - in `INPUT`, for traffic addressed to the host's own IPs. `[INFERRED]` That traffic never passes `DOCKER-USER`.
- No control plane container (API, Worker, PostgreSQL, object storage) joins a build or app network. The one exception is Nginx, which is the public entry point anyway.

Rejected for builds:

- **No network at all:** installs break.
- **An allowlist proxy** (npm, PyPI, GitHub only): that is the upgrade path if exfiltration matters more than arbitrary dependencies.

### What this does not protect against

These limits are stated in the docs and in the dashboard:

1. **One shared kernel.** A kernel exploit from any build or app owns the host. Rootless BuildKit lowers where an escape lands, to an unprivileged user; it does not remove kernel bugs.
2. **Exfiltration over the allowed egress.** A build or app can send anything it can read to the internet. A build can read its own build-phase secrets, and npm `postinstall` scripts run with network access.
3. **Shared resources without a limit:** disk I/O, network bandwidth, CPU caches (side channels).
4. **Root-equivalent access on each node:** the `docker` group and the agent.

### Stronger isolation

Not now. M9 writes the evaluation (spec 19). Starting hypothesis `[INFERRED]`: gVisor for apps is the cheapest step up, because it is a Docker runtime switch (`--runtime=runsc`). Kata and Firecracker give VM boundaries and need KVM, which this host has.

## Why

- **Every control above is a plain Docker, BuildKit or Linux feature.** No custom sandbox code.
- **Builds need the network.** Blocking private ranges protects what the spec names (control plane, database, other tenants, metadata) and keeps installs working.
- **Honest limits beat false claims.** Spec 16 explicitly asks for them.

## Consequences

- **Host rules:** the iptables rules need sudo through `make init` and have to be reapplied after a reboot.
- **Hostile fixtures become the security tests (spec 10):** fork bomb, huge log, Docker socket probe, metadata IP and other-container probes, disk fill. M5 runs the build-side ones; M8 runs all of them (spec 19).
- **Stub until M8:** the network rules go into `docs/stubs.md`.
