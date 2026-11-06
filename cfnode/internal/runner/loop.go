// Package runner is `cfnode runner`: it claims jobs from the control plane and runs them (overview.md, section 4).
package runner

import (
	"context"
	"errors"
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
// YOU BUILD IT (M1): the rules are in docs/you-build-it/m1-claim-loop.md, and loop_test.go checks them.
func Loop(ctx context.Context, cfg Config, claimer Claimer, executor Executor) error {
	return errors.ErrUnsupported // the stub, so the tests skip: replace this line with your loop
}
