# YOU BUILD IT (M1): the runner's claim loop

Status: shown 2026-11-06. You chose SHOW ME plus a walkthrough: the loop in `cfnode/internal/runner/loop.go` is mine, and all 6 tests pass. You write Go later (M6's agent loop), with this as background.

## The rules (one test each)

1. **Run each claimed job in its own goroutine.** After a job, claim the next one at once while a slot is free.
2. **Never more than `Capacity` jobs at once, and never call `Claim` while every slot is busy.** A claim starts the job's lease (ADR-0003), so a job claimed early would wait while its lease runs out.
3. **"Nothing to run" (`nil, nil`): wait `PollInterval` ± 20 %, random each time.** Then many runners don't poll in lockstep.
4. **Any other error: wait, then try again.** The wait grows while errors repeat, never goes above `MaxBackoff` (jitter included), and goes back to `PollInterval` after a good answer.
5. **An error wrapping `api.ErrUnauthorized` (401): stop claiming.** Let the running jobs finish, then return the error.
6. **Context cancelled: stop claiming.** Let the running jobs finish (they have the same context, so they stop too), then return nil.

## Walkthrough of `Loop`, block by block

1. `slots := make(chan struct{}, cfg.Capacity)`. `:=` declares a variable and infers its type. This is a channel with room for `Capacity` items; `struct{}` is an empty value, so only the count matters. Putting an item in takes a slot; when the channel is full, the next put waits. In Python: `threading.Semaphore(capacity)`.
2. `var jobs sync.WaitGroup`, then `defer jobs.Wait()`. The WaitGroup counts the running job goroutines. `defer` runs `Wait` when `Loop` returns, whichever way it returns: Python's `try/finally` plus joining every thread (rules 5 and 6).
3. `for { ... }`. Go's only loop keyword. With nothing after it, it is `while True:`.
4. The first `select` waits for whichever comes first: a free slot, or `ctx.Done()` (the cancel). Taking the slot before `Claim` is rule 2.
5. `job, err := claimer.Claim(ctx)`. Two results: Go returns errors as values instead of raising them. `job` is a pointer (`*api.Claim`); `nil` means no job, like Python's `None`.
6. A job came. Reset the backoff, then `jobs.Go(func() { ... })` starts a goroutine and counts it. Inside, `defer func() { <-slots }()` takes one item out of the channel when the job ends, so the slot is free again. `*job` copies the claim out of the pointer. `continue` starts the next round at once (rule 1).
7. No job. `<-slots` gives the slot back. A `switch` with no value is an if/elif/else chain:
   - a 401 returns the error (rule 5);
   - another error waits `backoff` and doubles it for next time, capped at `MaxBackoff` (rule 4). `a, b = x, y` works like Python's tuple assignment;
   - nothing to run waits about 1 s, with jitter (rule 3).
8. The second `select` is a sleep that a cancel interrupts (rule 6).
9. `jitter` returns `0.8 × d` plus a random amount below `0.4 × d`. A `time.Duration` is a count of nanoseconds, so integer arithmetic works.

Why this is safe with many goroutines: only the loop itself touches `backoff` and `wait`. The job goroutines share only the channel, which is built for that, and `ctx` and `executor`, which they only read.

## Go for this task (10 min)

| In Python | In Go |
|---|---|
| start a thread | `go f()` starts a goroutine. They are cheap: thousands are fine. |
| `queue.Queue(maxsize=n)` | `ch := make(chan T, n)`. A send `ch <- v` blocks while it is full; a receive `<-ch` blocks while it is empty. |
| wait for whichever comes first | `select { case <-a: ... case b <- v: ... case <-ctx.Done(): ... }` runs the first case that is ready. |
| a `threading.Event` that means "stop" | `ctx context.Context`. `ctx.Done()` is a channel that closes on cancel; `ctx.Err()` says why. |
| `join()` on many threads | `var wg sync.WaitGroup`, then `wg.Go(func() { ... })` for each, then `wg.Wait()`. |
| a sleep that a stop interrupts | `case <-time.After(d):` in a `select` next to `case <-ctx.Done():`. |
| `isinstance(e, X)`, through wrapped errors | `errors.Is(err, api.ErrUnauthorized)`. |
| `random.uniform(0, d)` | `rand.N(d)` from `math/rand/v2`: a random `time.Duration` in [0, d). |
| `min(a, b)` | `min(a, b)`, built in. |

- `defer f()` runs `f` when the function returns, whichever way it returns.
- `make test-go` also runs `gofmt`, `go vet` and the race detector. Two goroutines touching the same variable without a channel or a lock fail the run.

## How the tests work

- `testing/synctest` gives each test a fake clock. `time.Sleep`, `time.After` and context timeouts jump forward at once whenever every goroutine is waiting. A minute of polling takes milliseconds, and every wait is measured exactly.
- `claimer` (in `loop_test.go`) plays a script of answers and records when each call came. `executor` pretends that each job takes a while, and counts how many run at once.
- A failing test's message names the rule that broke.
- I checked the tests against my own version and against 5 broken ones: each broken one fails the test of the rule it breaks.

## Why CloudForge needs exactly this

- Rules 1 and 2 keep a runner's builds within what its buildkitd can take (ADR-0009, one buildkitd per runner).
- Rules 3 and 4 keep 1-second polling cheap for the control plane, even when the API is down for a while.
- Rules 5 and 6 mean a runner never leaves half-run builds behind when it stops. In M2 you add the other half: leases and fencing.
