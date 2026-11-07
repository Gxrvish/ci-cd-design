package runner

// Tests for Log, each in a synctest bubble: the clock is fake, so the waits are measured exactly.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"cloudforge/cfnode/internal/api"
)

// sent is one chunk that reached the fake API, and when.
type sent struct {
	seq  int
	text string
	at   time.Duration // since the test started
}

// fakeLogAPI records each chunk it receives. It refuses the first `failures` calls with a temporary error, or
// every call with `refusal` when that is set.
type fakeLogAPI struct {
	start    time.Time
	failures int
	refusal  error
	mu       sync.Mutex
	calls    []sent
}

func (f *fakeLogAPI) send(_ context.Context, seq int, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sent{seq, text, time.Since(f.start)})
	if f.refusal != nil {
		return f.refusal
	}
	if len(f.calls) <= f.failures {
		return errors.New("503 Service Unavailable")
	}
	return nil
}

// upload starts log.Upload in a goroutine, with at most a minute of fake time: a stuck Upload fails the test
// at once instead of hanging. The returned function closes the log and returns Upload's error.
func upload(t *testing.T, log *Log, api *fakeLogAPI) func() error {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	done := make(chan error)
	go func() { done <- log.Upload(ctx, api.send) }()
	return func() error { log.Close(); return <-done }
}

func TestLogSendsWhatCameAtLeastOncePerSecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, fake := NewLog(1<<20), &fakeLogAPI{start: time.Now()}
		finish := upload(t, log, fake)

		io.WriteString(log, "a\n")
		time.Sleep(500 * time.Millisecond)
		io.WriteString(log, "b\n")
		time.Sleep(700 * time.Millisecond)
		io.WriteString(log, "c")
		if err := finish(); err != nil {
			t.Fatal(err)
		}

		want := []sent{{0, "a\nb\n", time.Second}, {1, "c", 1200 * time.Millisecond}}
		if fmt.Sprint(fake.calls) != fmt.Sprint(want) {
			t.Fatalf("sent %v, want %v: what came in the first second, then the rest at once on Close", fake.calls, want)
		}
	})
}

func TestLogCutsLongOutputAfterANewline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, fake := NewLog(1<<20), &fakeLogAPI{start: time.Now()}
		finish := upload(t, log, fake)
		output := strings.Repeat(strings.Repeat("x", 99)+"\n", 1000) // 100,000 bytes

		io.WriteString(log, output)
		if err := finish(); err != nil {
			t.Fatal(err)
		}

		var joined strings.Builder
		for i, c := range fake.calls {
			if c.seq != i || len(c.text) > 64<<10 || !strings.HasSuffix(c.text, "\n") {
				t.Fatalf("chunk %d: seq %d, %d bytes: want seq %d, at most 64 KiB, ending with a newline", i, c.seq, len(c.text), i)
			}
			joined.WriteString(c.text)
		}
		if len(fake.calls) != 2 || joined.String() != output {
			t.Fatalf("%d chunks; want 2 that together are the output", len(fake.calls))
		}
	})
}

func TestLogReplacesWhatTheDatabaseCantStore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, fake := NewLog(1<<20), &fakeLogAPI{start: time.Now()}
		finish := upload(t, log, fake)

		io.WriteString(log, "nul\x00 bad\xff ok ü\n")
		if err := finish(); err != nil {
			t.Fatal(err)
		}

		if want := "nul� bad� ok ü\n"; len(fake.calls) != 1 || fake.calls[0].text != want {
			t.Fatalf("sent %v, want %q", fake.calls, want)
		}
	})
}

func TestLogSendsAFailedChunkAgainUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, fake := NewLog(1<<20), &fakeLogAPI{start: time.Now(), failures: 2}
		finish := upload(t, log, fake)

		io.WriteString(log, "x")
		if err := finish(); err != nil {
			t.Fatal(err)
		}

		// Close at once: the first try right away, then after 100 ms and 200 ms more.
		want := []sent{{0, "x", 0}, {0, "x", 100 * time.Millisecond}, {0, "x", 300 * time.Millisecond}}
		if fmt.Sprint(fake.calls) != fmt.Sprint(want) {
			t.Fatalf("sent %v, want %v: the same seq and text, with waits that double", fake.calls, want)
		}
	})
}

func TestLogKeepsAtMostItsLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, fake := NewLog(8), &fakeLogAPI{start: time.Now()}
		finish := upload(t, log, fake)

		for _, s := range []string{"1234567\n", "more\n", "and more\n"} {
			if n, err := io.WriteString(log, s); n != len(s) || err != nil {
				t.Fatalf("Write(%q) = %d, %v: past the limit too, report success, so the build goes on", s, n, err)
			}
		}
		if err := finish(); err != nil {
			t.Fatal(err)
		}

		if want := "1234567\n" + limitLine; len(fake.calls) != 1 || fake.calls[0].text != want {
			t.Fatalf("sent %v, want %q: 8 bytes, then the limit line once", fake.calls, want)
		}
	})
}

func TestLogStopsWhenTheAttemptIsFenced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		refusal := fmt.Errorf("POST .../logs: %w", api.ErrFenced)
		log, fake := NewLog(1<<20), &fakeLogAPI{start: time.Now(), refusal: refusal}
		finish := upload(t, log, fake)

		io.WriteString(log, "x")
		if err := finish(); !errors.Is(err, api.ErrFenced) || len(fake.calls) != 1 {
			t.Fatalf("Upload returned %v after %d calls; want ErrFenced after 1: a 409 can't change on retry", err, len(fake.calls))
		}
	})
}
