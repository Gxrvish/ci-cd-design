# M0 internals lab: a container by hand

Status: run by you 2026-11-06 (short version). Time: about 15 min. One terminal, no sudo.

- **Goal:** a container is a process. Namespaces decide what it can see; cgroups decide what it can use. Spec M0: "run a container by hand with `unshare` and cgroup limits".
- **Checked:** I ran every step on this host first `[RAN]`. The comments show that run's output.
- **Why short:** the first sheet (45 min, a chroot filesystem, two terminals) felt awkward. This one keeps only what CloudForge's decisions rest on.

## 1. Root that isn't root (3 min): ADR-0009, ADR-0012

```sh
mkdir ~/cf-lab && cd ~/cf-lab
unshare --user --map-root-user sh -c 'id -u; touch by-unshare /etc/cf-lab'   # 0, then "Permission denied" for /etc
docker run --rm -v "$PWD":/x ubuntu:noble sh -c 'id -u; touch /x/by-docker' # 0 again
ls -ln                                   # by-unshare owned by 1000 (you), by-docker owned by 0 (real root)
```

## 2. A container by hand: namespaces + cgroup limits (8 min): ADR-0012, threat #20

```sh
systemd-run --user --scope -p MemoryMax=50M -p MemorySwapMax=0 -p TasksMax=20 \
  unshare --user --map-root-user --pid --fork --mount-proc --net bash
# now inside:
ps -o pid,user,comm                      # bash is PID 1, user "root"
cat /proc/net/dev                        # only lo: no network
CG=/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)
cat $CG/memory.max $CG/pids.max          # 52428800 and 20: limits are files
head -c 200m /dev/zero | tail            # Killed (echo $? shows 137)
grep oom_kill $CG/memory.events          # oom_kill 1: proof of the kill
sh -c 'for i in $(seq 30); do sleep 5 & done'   # Cannot fork
cat $CG/pids.events                      # max 1
exit
```

## 3. What Docker adds (3 min): ADR-0010, contract `DeploymentSpec.image`

```sh
docker run --rm ubuntu:noble cat /proc/net/dev   # lo + eth0: Docker connected a network
docker save ubuntu:noble | tar -t                # blobs/sha256/...: an image is hashed files
cd ~ && rm -rf ~/cf-lab
```

## Explain back (answer in chat; I grade, then write the learning log)

1. In step 1 both shells said uid 0. Why could Docker's root create a root-owned file in your folder, while unshare's root could not even touch `/etc`? Hostile code escapes a rootless BuildKit RUN step (ADR-0009); other hostile code escapes a rootful app container (ADR-0012). Who is each one on the host? Today's buildkitd finding counts.
2. What is a cgroup, physically? How did your bash end up in one without you writing any PID? Why did only 17 `sleep`s fit under a limit of 20?
3. Out of memory, the kernel killed `tail` and left `bash` alone. Why `tail`? What does exit code 137 encode? On this host (32 GB swap), what would have happened without `MemorySwapMax=0`?
4. Step 2's container had only `lo`; step 3's had `eth0`. What did Docker create to give it `eth0` and a way out to the internet (hint: a virtual cable and a virtual switch)? Why does CloudForge give every environment its own network instead of one shared one (ADR-0010, spec 12)?
5. `docker save` listed `blobs/sha256/<hex>` files. What are they? Why does CloudForge deploy `image@sha256:...` and never a tag like `:latest`?
