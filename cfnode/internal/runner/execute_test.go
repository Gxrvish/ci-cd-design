package runner

// Tests for Builder.Execute, with a fake API and a fake build engine, each in a synctest bubble (a fake clock).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloudforge/cfnode/internal/api"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var testJob = api.Claim{
	JobID: 7, Attempt: 1, TimeoutSeconds: 600, LogLimitBytes: 1 << 20,
	Source: api.Source{CommitSHA: "3f78"},
	Image:  api.ImageTarget{Ref: "registry:5000/default/hello:3f78-b1-a1"},
}

// fakeJobAPI records the log it receives, and every completion.
type fakeJobAPI struct {
	logRefusal    error  // when set, the answer to every log chunk
	onLog         func() // when set, called once a chunk is stored
	lostCompletes int    // this many completions fail first, with a temporary error
	mu            sync.Mutex
	log           strings.Builder
	completions   []api.Completion
}

func (f *fakeJobAPI) AppendLog(_ context.Context, _ api.Claim, _ int, text string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logRefusal != nil {
		return false, f.logRefusal
	}
	f.log.WriteString(text)
	if f.onLog != nil {
		f.onLog()
	}
	return false, nil
}

func (f *fakeJobAPI) Complete(_ context.Context, _ api.Claim, done api.Completion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions = append(f.completions, done)
	if len(f.completions) <= f.lostCompletes {
		return errors.New("503 Service Unavailable")
	}
	return nil
}

// step is a fake build step: it writes the build's output to log, and returns the image's digest.
type step = func(ctx context.Context, log io.Writer) (string, error)

// builder returns a Builder on a fake engine: its clone step returns cloneErr, and its build step is image.
func builder(fake *fakeJobAPI, cloneErr error, image step) *Builder {
	return &Builder{
		API:      fake,
		Checkout: func(context.Context, api.Source, string) (string, error) { return "", cloneErr },
		Image:    func(ctx context.Context, _, _ string, log io.Writer) (string, error) { return image(ctx, log) },
	}
}

// built is a build step that succeeds at once.
func built(_ context.Context, log io.Writer) (string, error) {
	io.WriteString(log, "#1 building\n")
	return testDigest, nil
}

// stuck is a build step that prints one line, then runs until it is cancelled.
func stuck(ctx context.Context, log io.Writer) (string, error) {
	io.WriteString(log, "#1 building\n")
	<-ctx.Done()
	return "", ctx.Err()
}

func TestExecuteBuildsTheCheckoutAndCompletesWithTheDigest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeJobAPI{}
		var checkout, buildDir, pushed string
		b := &Builder{
			API: fake,
			Checkout: func(_ context.Context, src api.Source, dir string) (string, error) {
				checkout = dir
				return dir + "/" + src.CommitSHA, nil
			},
			Image: func(ctx context.Context, dir, ref string, log io.Writer) (string, error) {
				buildDir, pushed = dir, ref
				return built(ctx, log)
			},
		}

		b.Execute(t.Context(), testJob)

		want := []api.Completion{{Result: "succeeded", ImageDigest: testDigest}}
		if !slices.Equal(fake.completions, want) || fake.log.String() != "#1 building\n" {
			t.Fatalf("completed %+v with log %q; want %+v and the build's output", fake.completions, fake.log.String(), want)
		}
		if buildDir != checkout+"/3f78" || pushed != testJob.Image.Ref {
			t.Errorf("built %q as %q; want the folder that Checkout returned, as the claim's image", buildDir, pushed)
		}
		if _, err := os.Stat(checkout); !os.IsNotExist(err) {
			t.Errorf("the checkout %s is still there (%v): delete it when the job is over", checkout, err)
		}
	})
}

func TestExecuteNamesTheStepThatFailed(t *testing.T) {
	failing := func(context.Context, io.Writer) (string, error) { return "", errors.New("exit code: 1") }
	for _, c := range []struct {
		cloneErr    error
		image       step
		reason, why string
	}{
		{errors.New("not our ref"), built, "step_failed:clone", "clone failed: not our ref"},
		{nil, failing, "step_failed:build", "build failed: exit code: 1"},
	} {
		synctest.Test(t, func(t *testing.T) {
			fake := &fakeJobAPI{}

			builder(fake, c.cloneErr, c.image).Execute(t.Context(), testJob)

			want := []api.Completion{{Result: "failed", Reason: c.reason}}
			if !slices.Equal(fake.completions, want) || !strings.Contains(fake.log.String(), c.why) {
				t.Errorf("completed %+v with log %q; want %+v, and a log that says %q: the build's reader needs the error",
					fake.completions, fake.log.String(), want, c.why)
			}
		})
	}
}

func TestExecuteStopsABuildAtItsTimeLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, start := &fakeJobAPI{}, time.Now()
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour) // without a time limit, fail instead of hanging
		defer cancel()

		builder(fake, nil, stuck).Execute(ctx, testJob)

		want := []api.Completion{{Result: "failed", Reason: "timeout"}}
		if !slices.Equal(fake.completions, want) || time.Since(start) != 600*time.Second {
			t.Fatalf("completed %+v after %v; want %+v after the claim's 600 s", fake.completions, time.Since(start), want)
		}
		if !strings.Contains(fake.log.String(), "time limit of 600 s") {
			t.Errorf("the log %q doesn't say that the build passed its time limit", fake.log.String())
		}
	})
}

func TestExecuteDropsAFencedAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake, start := &fakeJobAPI{logRefusal: fmt.Errorf("POST .../logs: %w", api.ErrFenced)}, time.Now()

		builder(fake, nil, stuck).Execute(t.Context(), testJob)

		// The first chunk goes out after 1 s. Its refusal must stop the build, long before the time limit.
		if len(fake.completions) != 0 || time.Since(start) != time.Second {
			t.Fatalf("%d completions after %v; want none after 1 s: a fenced attempt stops, and reports nothing",
				len(fake.completions), time.Since(start))
		}
	})
}

func TestExecuteReportsNothingWhenTheRunnerStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { // the runner stops in the middle of the build
		fake, start := &fakeJobAPI{}, time.Now()
		ctx, stopRunner := context.WithCancel(t.Context())
		time.AfterFunc(5*time.Second, stopRunner)

		builder(fake, nil, stuck).Execute(ctx, testJob)

		if len(fake.completions) != 0 || time.Since(start) != 5*time.Second {
			t.Fatalf("completed %+v after %v; want nothing after 5 s: the runner cut the build short, so it didn't fail",
				fake.completions, time.Since(start))
		}
	})
	synctest.Test(t, func(t *testing.T) { // the runner stops after the build, while the last of the log is on its way
		ctx, stopRunner := context.WithCancel(t.Context())
		fake := &fakeJobAPI{onLog: stopRunner}

		builder(fake, nil, built).Execute(ctx, testJob)

		if len(fake.completions) != 0 {
			t.Fatalf("completed %+v; want nothing: a runner that is stopping makes no more calls", fake.completions)
		}
	})
}

func TestExecuteSendsTheCompletionAgainUntilItGetsThrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeJobAPI{lostCompletes: 2}

		builder(fake, nil, built).Execute(t.Context(), testJob)

		done := api.Completion{Result: "succeeded", ImageDigest: testDigest}
		if want := []api.Completion{done, done, done}; !slices.Equal(fake.completions, want) {
			t.Fatalf("completions %+v; want the same one 3 times: the API accepts a repeat (ADR-0003 rule 6)", fake.completions)
		}
	})
}
