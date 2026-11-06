# Learning log

One entry per milestone: what you did, what it showed, the explain-back result, and a real-world note.

## M0: design gates and a container by hand (2026-11-06)

- **Did:** Gates A, B and C (approved 2026-11-05 and 2026-11-06). The internals lab `docs/labs/m0-container-by-hand.md`, short version, run by you on 2026-11-06. Done 16 days before the plan's Nov 22.
- **What the lab showed** (`[RAN]` my dry run on this host; you ran the same commands, output not pasted):
  1. Root in a user namespace is uid 1000 outside: it can't write `/etc`, and its files belong to you. Root in a Docker container is real root: its file belonged to uid 0.
  2. A cgroup is a directory, and its limits are files: `memory.max` 52428800, `pids.max` 20.
  3. Over the memory limit, the kernel killed `tail` (exit 137) and counted it (`memory.events`: `oom_kill 1`). At the pids limit, fork failed (`pids.events`: `max 1`).
  4. A new network namespace has only `lo`. Docker adds `eth0`.
  5. An image is a set of files named by their SHA-256 (`docker save`).
- **Explain-back:** skipped (you replied "go"). The 5 questions stay in the lab sheet. Answer any time and I grade them.
- **Found on the way:**
  - buildkitd's uid is your login uid on this host (`docs/progress.md`, open questions).
  - A limit holds only if the limited code can't reach its knob `[RAN]`:
    - In the lab's container, `echo max > $CG/memory.max` worked: you owned that cgroup, and `/sys/fs/cgroup` was writable.
    - In a Docker container the same write fails: "Read-only file system" (Docker mounts the cgroup filesystem `ro`).
    - The M1 lab checks the same for a RUN step (`docs/operations/local-setup.md`).

## Real-world note (M0)

- `[SOURCE]` BuildKit runs each RUN step from an OCI runtime spec that it generates (`executor/oci/spec.go:87`, moby/buildkit `b9d02e6`, Apache-2.0):
  - It adds a cgroup namespace when cgroup v2 supports one (`spec.go:178-182`).
  - It adds a user namespace with UID and GID maps when it has an ID mapping (`spec_linux.go:112-117`).
  - With the process sandbox off, the step shares the host's PID namespace (`spec_linux.go:100-105`).

  These are the namespaces you created with `unshare`; runc creates them from the spec.
- `[SOURCE]` Odoo limits each worker's memory with `setrlimit(RLIMIT_AS)`. It sets the soft limit and keeps the hard one (`odoo/service/server.py:72-82`, LGPL-3, `5268d45f`).
  - `[RAN]` A process can raise its own soft limit back up to the hard limit, so a hostile worker could undo it.
  - Each process also gets its own budget. A cgroup limit covers the whole process tree, and holds against code that can't write the cgroup's files.
