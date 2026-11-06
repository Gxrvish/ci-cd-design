# M1 internals lab: BuildKit by hand, and its cache

Status: run by me for you 2026-11-06; you chose to read the output instead of typing the commands. Time: about 15 min. One terminal, no sudo.

- **Goal:** build an image with buildkitd directly, the way the runner will, and see when BuildKit reuses work (spec M1).
- **Checked:** I ran every step on this host first `[RAN]`, with BuildKit v0.34.2. The comments show that run's output.
- **Shell:** the lab uses bash syntax, and your login shell is fish. Start with `bash`.

## 0. Start a rootless buildkitd (3 min): ADR-0009

```sh
bash
mkdir -p ~/cf-lab/app && cd ~/cf-lab
printf 'FROM busybox:1.37\nRUN echo installing && sleep 3\nCOPY app.txt /app.txt\n' > app/Dockerfile
echo v1 > app/app.txt
docker run -d --name cf-lab-bk --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
  --security-opt systempaths=unconfined -v "$PWD":/lab:ro moby/buildkit:v0.34.2-rootless
sleep 3
```

A helper that builds one folder and prints each step as `CACHED` or `ran`. Paste it as is:

```sh
b() { docker exec cf-lab-bk buildctl build --frontend dockerfile.v0 --local context=/lab/$1 --local dockerfile=/lab/$1 --progress plain 2>&1 |
  awk '/^#[0-9]+ \[[0-9]+\/[0-9]+\] (RUN|COPY)/ {id=$1; sub(/^#[0-9]+ /, ""); step[id]=$0; order[++n]=id}
       /^#[0-9]+ CACHED/ {cached[$1]=1}
       / ERROR/ {print}
       END {for (i = 1; i <= n; i++) print (cached[order[i]] ? "CACHED " : "ran    ") step[order[i]]}'; }
```

## 1. Cold, then warm (2 min)

```sh
time b app    # ran RUN, ran COPY; about 7 s (includes pulling busybox)
b app         # CACHED RUN, CACHED COPY
```

## 2. What breaks the cache (3 min)

```sh
echo v2 > app/app.txt; b app    # CACHED RUN, ran COPY: the file's content changed
touch app/app.txt; b app        # CACHED, CACHED: same content, so a new mtime changes nothing
```

## 3. A second project (2 min): ADR-0009, threat #19

```sh
cp -r app app2; echo other > app2/app.txt; b app2   # CACHED RUN: app2 reused app's work
```

Everything built by one buildkitd shares its cache. That is why each runner has its own buildkitd, one organization and one pool.

## 4. Order matters (3 min): ADR-0008

```sh
printf 'FROM busybox:1.37\nCOPY app.txt /app.txt\nRUN echo installing && sleep 3\n' > app/Dockerfile
b app                            # ran, ran: new order, new cache keys
echo v3 > app/app.txt; b app     # ran COPY, ran RUN: every source change now repeats the slow step
```

A step's cache key covers its instruction, its inputs and every step before it. Slow, stable steps (installing dependencies) go first, and copying the source goes last.

## 5. The cache is on disk (2 min)

```sh
docker exec cf-lab-bk buildctl du | tail -1   # Total: 6.88MB
docker exec cf-lab-bk buildctl prune | tail -1
b app                                         # ran, ran: nothing left to reuse
docker rm -f cf-lab-bk; cd ~ && rm -rf ~/cf-lab; exit
```

The 5 explain-back questions come at the end of M1, after the break-it session.
