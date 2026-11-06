// Package runner is `cfnode runner`: it claims jobs from the control plane and runs them (overview.md, section 4).
package runner

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"cloudforge/cfnode/internal/api"
)

// Claimer asks the control plane for one job: POST /v1/jobs/claim.
type Claimer interface {
	// Claim returns the claimed job, or nil when there is nothing to run (204).
	// An error wrapping api.ErrUnauthorized means the credential was refused: stop for good.
	// Any other error is temporary: try again later.
	Claim(ctx context.Context) (*api.Claim, error)
}

// Executor runs one claimed job to its end: clone, build, push, complete.
// It returns when the job is over, or soon after ctx is cancelled.
type Executor interface {
	Execute(ctx context.Context, job api.Claim)
}

// Config tunes Loop. In production: Capacity as registered, PollInterval 1 s, MaxBackoff 2 s (contracts/v1.yaml).
type Config struct {
	Capacity     int           // jobs at once
	PollInterval time.Duration // wait after "nothing to run"
	MaxBackoff   time.Duration // longest wait after errors
}

// Loop claims jobs and runs each one in its own goroutine, until ctx is cancelled.
// It returns nil once ctx is cancelled and every job it started has returned,
// or an error wrapping api.ErrUnauthorized when the control plane refuses the credential.
//
// YOU BUILD IT (M1), shown on request (SHOW ME): docs/you-build-it/m1-claim-loop.md has the rules and a walkthrough.
func Loop(ctx context.Context, cfg Config, claimer Claimer, executor Executor) error {
	slots := make(chan struct{}, cfg.Capacity) // one item per running job: full means every slot is busy
	var jobs sync.WaitGroup
	defer jobs.Wait() // however Loop returns, it waits for the jobs it started

	backoff := cfg.PollInterval
	for {
		select { // take a slot before claiming: a claim starts the job's lease (ADR-0003)
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}

		job, err := claimer.Claim(ctx)
		if err == nil && job != nil {
			backoff = cfg.PollInterval
			jobs.Go(func() {
				defer func() { <-slots }() // the job is over: free its slot
				executor.Execute(ctx, *job)
			})
			continue // claim the next one at once
		}
		<-slots // no job: give the slot back

		var wait time.Duration
		switch {
		case errors.Is(err, api.ErrUnauthorized):
			return err
		case err != nil: // temporary: wait longer each time, up to MaxBackoff
			wait, backoff = backoff, min(2*backoff, cfg.MaxBackoff)
		default: // nothing to run
			wait, backoff = jitter(cfg.PollInterval), cfg.PollInterval
		}

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
	}
}

// jitter returns d ± 20 %, so that many runners don't poll in lockstep.
func jitter(d time.Duration) time.Duration {
	return d*8/10 + rand.N(d*4/10)
}
