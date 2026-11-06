# YOU BUILD IT (M1): the runner's claim loop

Status: ready 2026-11-06. Time: about 2 to 3 h, including the Go below. Stuck? Say **HINT** (one hint at a time) or **SHOW ME** (my version).

## The task

1. Open `cfnode/internal/runner/loop.go` and replace the one-line stub in `Loop`.
2. Run `make test-go` (about 5 s). The 6 tests skip while the stub is there. Then all 6 must pass.
3. Say "done". I review it (REVIEW MODE), then wire it into `cfnode runner`.

Everything else exists: the interfaces, the fakes and the tests. You need only the standard library.

## The rules (one test each)

1. **Run each claimed job in its own goroutine.** After a job, claim the next one at once while a slot is free.
2. **Never more than `Capacity` jobs at once, and never call `Claim` while every slot is busy.** A claim starts the job's lease (ADR-0003), so a job claimed early would wait while its lease runs out.
3. **"Nothing to run" (`nil, nil`): wait `PollInterval` ± 20 %, random each time.** Then many runners don't poll in lockstep.
4. **Any other error: wait, then try again.** The wait grows while errors repeat, never goes above `MaxBackoff` (jitter included), and goes back to `PollInterval` after a good answer.
5. **An error wrapping `api.ErrUnauthorized` (401): stop claiming.** Let the running jobs finish, then return the error.
6. **Context cancelled: stop claiming.** Let the running jobs finish (they have the same context, so they stop too), then return nil.

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
