package runner

// Acceptance tests for Loop (YOU BUILD IT, M1). Each test runs in a synctest bubble: the clock is fake,
// so a minute of polling takes milliseconds, and every wait is measured exactly.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"cloudforge/cfnode/internal/api"
)

var production = Config{Capacity: 1, PollInterval: time.Second, MaxBackoff: 2 * time.Second}

// result is one answer of the fake control plane: a job, an error, or neither (nothing to run).
type result struct {
	job *api.Claim
	err error
}

// claimer answers with its script, then "nothing to run" forever. It records when it was asked.
type claimer struct {
	mu     sync.Mutex
	script []result
	calls  []time.Time
}

func (c *claimer) Claim(context.Context) (*api.Claim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, time.Now())
	if len(c.script) == 0 {
		return nil, nil
	}
	next := c.script[0]
	c.script = c.script[1:]
	return next.job, next.err
}

func (c *claimer) gaps() []time.Duration {
	var gaps []time.Duration
	for i := 1; i < len(c.calls); i++ {
		gaps = append(gaps, c.calls[i].Sub(c.calls[i-1]))
	}
	return gaps
}

// executor pretends that every job takes `takes`, or ends earlier when its ctx is cancelled.
type executor struct {
	takes      time.Duration
	mu         sync.Mutex
	running    int
	maxRunning int
	finished   []int64
}

func (e *executor) Execute(ctx context.Context, job api.Claim) {
	e.mu.Lock()
	e.running++
	e.maxRunning = max(e.maxRunning, e.running)
	e.mu.Unlock()
	select {
	case <-time.After(e.takes):
	case <-ctx.Done():
	}
	e.mu.Lock()
	e.running--
	e.finished = append(e.finished, job.JobID)
	e.mu.Unlock()
}

func jobs(n int) []result {
	script := make([]result, n)
	for i := range script {
		script[i] = result{job: &api.Claim{JobID: int64(i + 1), Attempt: 1}}
	}
	return script
}

func failures(n int) []result {
	script := make([]result, n)
	for i := range script {
		script[i] = result{err: errors.New("503 Service Unavailable")}
	}
	return script
}

// stub is set while Loop is still the stub, so the tests skip instead of failing.
var stub atomic.Bool

// bubble runs a test in a synctest bubble, where the clock is fake.
func bubble(t *testing.T, test func(t *testing.T)) {
	t.Helper()
	synctest.Test(t, test)
	if stub.Load() {
		t.Skip("YOU BUILD IT: replace the stub in loop.go (docs/you-build-it/m1-claim-loop.md)")
	}
}

// run gives Loop `d` of fake time, then cancels its context, and returns what Loop returned.
func run(t *testing.T, cfg Config, c *claimer, e *executor, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	defer cancel()
	err := Loop(ctx, cfg, c, e)
	if errors.Is(err, errors.ErrUnsupported) {
		stub.Store(true)
		t.SkipNow()
	}
	return err
}

func TestRunsClaimedJobsAtOnceAndInParallel(t *testing.T) {
	bubble(t, func(t *testing.T) {
		c, e := &claimer{script: jobs(3)}, &executor{takes: time.Minute}
		start := time.Now()

		if err := run(t, Config{Capacity: 3, PollInterval: time.Second, MaxBackoff: 2 * time.Second}, c, e, 2*time.Minute); err != nil {
			t.Fatalf("Loop returned %v after its context was cancelled, want nil", err)
		}
		if len(e.finished) != 3 || e.maxRunning != 3 {
			t.Fatalf("ran %v, at most %d at once; want all 3 jobs at once", e.finished, e.maxRunning)
		}
		for i := range 3 {
			if waited := c.calls[i].Sub(start); waited != 0 {
				t.Fatalf("claim %d came after %v: after a job, claim the next one at once while a slot is free", i+1, waited)
			}
		}
	})
}

func TestNeverClaimsWhileEverySlotIsBusy(t *testing.T) {
	bubble(t, func(t *testing.T) {
		c, e := &claimer{script: jobs(5)}, &executor{takes: 10 * time.Second}
		start := time.Now()

		run(t, Config{Capacity: 2, PollInterval: time.Second, MaxBackoff: 2 * time.Second}, c, e, time.Minute)

		if e.maxRunning != 2 || len(e.finished) != 5 {
			t.Fatalf("ran %v, at most %d at once; want all 5 jobs, at most 2 at once", e.finished, e.maxRunning)
		}
		if third := c.calls[2].Sub(start); third < 10*time.Second {
			t.Fatalf("third claim at %v, but both slots were busy until 10s: a claim starts the job's lease (ADR-0003)", third)
		}
	})
}

func TestPollsAboutOnceASecondWhenIdle(t *testing.T) {
	bubble(t, func(t *testing.T) {
		c := &claimer{}

		run(t, production, c, &executor{}, time.Minute)

		gaps := c.gaps()
		for i, gap := range gaps {
			if gap < 800*time.Millisecond || gap > 1200*time.Millisecond {
				t.Fatalf("wait %d was %v, want 1 s ± 20 %% (contracts/v1.yaml, claim 204)", i+1, gap)
			}
		}
		if len(gaps) < 2 || allEqual(gaps) {
			t.Fatalf("waits %v: add random jitter, so that many runners don't poll in lockstep", gaps)
		}
	})
}

func TestBacksOffOnTemporaryErrorsUpToTheCap(t *testing.T) {
	bubble(t, func(t *testing.T) {
		c := &claimer{script: failures(6)}

		run(t, Config{Capacity: 1, PollInterval: 100 * time.Millisecond, MaxBackoff: 2 * time.Second}, c, &executor{}, time.Minute)

		gaps := c.gaps() // gaps[0:6] follow the 6 errors, gaps[6] follows the first good answer
		for i, gap := range gaps[:6] {
			if gap < 80*time.Millisecond || gap > 2*time.Second {
				t.Fatalf("retry %d came after %v: wait at least about PollInterval and at most MaxBackoff", i+1, gap)
			}
		}
		if gaps[5] <= 2*gaps[0] {
			t.Fatalf("waits after errors were %v: they should grow while errors repeat", gaps[:6])
		}
		if gaps[6] > 120*time.Millisecond {
			t.Fatalf("after a good answer the wait was %v: go back to PollInterval", gaps[6])
		}
	})
}

func TestStopsForGoodOnUnauthorizedAfterItsJobsEnd(t *testing.T) {
	bubble(t, func(t *testing.T) {
		refused := result{err: fmt.Errorf("claim: %w", api.ErrUnauthorized)}
		c, e := &claimer{script: append(jobs(1), refused)}, &executor{takes: 30 * time.Second}
		start := time.Now()

		err := run(t, Config{Capacity: 2, PollInterval: time.Second, MaxBackoff: 2 * time.Second}, c, e, time.Hour)

		if !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("Loop returned %v, want an error wrapping api.ErrUnauthorized", err)
		}
		if len(c.calls) != 2 {
			t.Fatalf("claimed %d times; a 401 means stop, retrying won't help", len(c.calls))
		}
		if e.running != 0 || len(e.finished) != 1 {
			t.Fatalf("Loop returned while its job still ran: wait for started jobs")
		}
		if took := time.Since(start); took > time.Minute {
			t.Fatalf("Loop returned after %v: return as soon as the running job ends", took)
		}
	})
}

func TestCancelStopsClaimingAndWaitsForJobs(t *testing.T) {
	bubble(t, func(t *testing.T) {
		c, e := &claimer{script: jobs(1)}, &executor{takes: time.Hour}
		start := time.Now()

		if err := run(t, production, c, e, 10*time.Second); err != nil {
			t.Fatalf("Loop returned %v after its context was cancelled, want nil", err)
		}
		if e.running != 0 || len(e.finished) != 1 {
			t.Fatalf("Loop returned while its job still ran: wait for started jobs")
		}
		if took := time.Since(start); took > 11*time.Second {
			t.Fatalf("Loop returned after %v: give each job the loop's context, so a cancel reaches it", took)
		}
		claims := len(c.calls)
		time.Sleep(10 * time.Second)
		if len(c.calls) != claims {
			t.Fatalf("something still claimed after Loop returned")
		}
	})
}

func allEqual(gaps []time.Duration) bool {
	for _, gap := range gaps {
		if gap != gaps[0] {
			return false
		}
	}
	return true
}
