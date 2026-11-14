package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"cloudforge/cfnode/internal/api"
)

// JobAPI is the part of the node API that one attempt calls, with its job token. *api.Client has these methods.
type JobAPI interface {
	AppendLog(ctx context.Context, job api.Claim, seq int, text string) (cancelRequested bool, err error)
	Complete(ctx context.Context, job api.Claim, done api.Completion) error
}

// Builder runs a claimed job from checkout to complete. It is the Executor that Loop needs.
//
// Checkout and Image are the build engine (package build). They are fields that hold functions, the way a Python
// attribute can hold a callable: the tests put fakes there, so they need neither Git nor buildkitd.
type Builder struct {
	API JobAPI
	// Checkout fetches the source's commit into dir and returns the build context inside it (build.Checkout).
	Checkout func(ctx context.Context, src api.Source, dir string) (string, error)
	// Image builds the Dockerfile in dir, pushes the image as ref and returns its digest. The build's output
	// goes to log (build.Image, with this runner's buildkitd and registry login).
	Image func(ctx context.Context, dir, ref string, log io.Writer) (string, error)
}

// Execute runs one attempt to its end, and tells the control plane how it ended (J3 or J4).
//
// It drops the attempt without completing it in two cases: the API refuses one of its calls (fenced: the attempt
// no longer owns the job), or ctx is cancelled (the runner is stopping). The job is then the control plane's to
// hand out again, which it does from M2 on (docs/stubs.md).
func (b *Builder) Execute(ctx context.Context, job api.Claim) {
	// slog is Go's logging module. With returns a logger that adds these key=value pairs to each line.
	attempt := slog.With("job", job.JobID, "attempt", job.Attempt)
	attempt.Info("job started", "commit", job.Source.CommitSHA)
	done, err := b.run(ctx, job)
	if err != nil {
		attempt.Warn("job dropped, not completed", "error", err)
		return
	}
	attempt.Info("job completed", "result", done.Result, "reason", done.Reason)
}

// run builds the job while a second goroutine uploads its log, then completes it. An error means that the attempt
// was dropped instead.
func (b *Builder) run(ctx context.Context, job api.Claim) (api.Completion, error) {
	ctx, stop := context.WithCancel(ctx) // stop cancels this attempt only, not the runner
	defer stop()

	output := NewLog(int(job.LogLimitBytes))
	uploaded := make(chan error, 1) // Upload's result. Room for one item, so the goroutine never waits to send it
	go func() {
		err := output.Upload(ctx, func(ctx context.Context, seq int, text string) error {
			_, err := b.API.AppendLog(ctx, job, seq, text) // the cancel flag is ignored until M2 (docs/stubs.md)
			return err
		})
		if err != nil {
			stop() // the API refused the log, so the attempt is over: stop its build too
		}
		uploaded <- err
	}()

	done := b.build(ctx, job, output)
	output.Close()
	// cmp.Or is Python's `a or b`: the upload's error, or else the reason why ctx ended, or else nil.
	if err := cmp.Or(<-uploaded, ctx.Err()); err != nil {
		return done, err // fenced, or the runner is stopping and may have cut the build short: report nothing
	}
	return done, retry(ctx, func() error { return b.API.Complete(ctx, job, done) })
}

// build runs the attempt's steps within the job's time limit, and returns how they ended. It writes a failure
// into the log too, for the person who reads the build.
func (b *Builder) build(ctx context.Context, job api.Claim, output io.Writer) api.Completion {
	// WithTimeout returns a ctx that is cancelled by itself once the time has passed, and its Err then says so.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(job.TimeoutSeconds)*time.Second)
	defer cancel()

	step, digest, err := b.steps(ctx, job, output)
	switch {
	case err == nil:
		return api.Completion{Result: "succeeded", ImageDigest: digest}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(output, "\n[CloudForge: stopped, because the build passed its time limit of %d s]\n", job.TimeoutSeconds)
		return api.Completion{Result: "failed", Reason: "timeout"}
	default:
		fmt.Fprintf(output, "\n[CloudForge: %s failed: %v]\n", step, err)
		return api.Completion{Result: "failed", Reason: "step_failed:" + step}
	}
}

// steps checks the commit out into a new folder, then builds and pushes it. It returns the image's digest, or the
// error and the name of the step that failed: clone or build. The folder is deleted when steps returns.
func (b *Builder) steps(ctx context.Context, job api.Claim, output io.Writer) (step, digest string, err error) {
	dir, err := os.MkdirTemp("", "cf-job-") // like Python's tempfile.mkdtemp
	if err != nil {
		return "clone", "", err
	}
	defer os.RemoveAll(dir)
	contextDir, err := b.Checkout(ctx, job.Source, dir)
	if err != nil {
		return "clone", "", err
	}
	digest, err = b.Image(ctx, contextDir, job.Image.Ref, output)
	return "build", digest, err
}
