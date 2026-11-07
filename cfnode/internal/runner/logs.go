package runner

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"cloudforge/cfnode/internal/api"
)

const (
	maxChunk   = 64 << 10    // bytes per chunk: the API refuses bigger ones (413)
	flushEvery = time.Second // send what has come at least this often (ADR-0006 rule 1)
	limitLine  = "\n[CloudForge: the log reached its size limit, so later output is not kept]\n"
)

// replacement stands in for bytes the database can't store as text: NUL, and invalid UTF-8.
var replacement = []byte("�")

// Log is one attempt's build output on its way to the control plane (ADR-0006 rules 1, 2 and 4).
// The build writes to it, as to a file; Upload sends what was written as chunks 0, 1, 2 and so on.
//
// Write and Upload run in different goroutines, so mu guards the fields below it, like a threading.Lock.
type Log struct {
	ready chan struct{} // wakes Upload before the next second: a full chunk is waiting, or the output ended

	mu      sync.Mutex
	pending []byte // cleaned output that no chunk holds yet
	kept    int    // bytes kept so far, counted against limit
	limit   int    // the claim's log_limit_bytes
	full    bool   // the limit was reached: nothing more is kept
	closed  bool   // the output ended: Upload sends the rest, then returns
}

// NewLog returns an empty log that keeps at most limit bytes of output.
func NewLog(limit int) *Log {
	return &Log{ready: make(chan struct{}, 1), limit: limit}
}

// Write keeps p for upload, with NUL bytes and invalid UTF-8 replaced by U+FFFD (rule 1). Once the output passes
// the limit, it keeps one line saying so, then nothing more (rule 4). Write never fails, so the build goes on.
//
// A character split across two writes is replaced too. The build engine's printer writes whole lines, except
// for a line still in progress, so this is rare.
func (l *Log) Write(p []byte) (int, error) {
	text := bytes.ReplaceAll(bytes.ToValidUTF8(p, replacement), []byte{0}, replacement)
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.full:
		return len(p), nil
	case l.kept+len(text) > l.limit:
		l.full, text = true, []byte(limitLine)
	default:
		l.kept += len(text)
	}
	l.pending = append(l.pending, text...)
	if len(l.pending) >= maxChunk {
		l.wake()
	}
	return len(p), nil // len(p), not len(text): the caller wrote all of p
}

// Close marks the end of the output. Upload then sends the rest at once, and returns.
func (l *Log) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.wake()
}

// wake tells Upload to send now. The channel holds one signal, so a signal sent while Upload is busy waits for it.
func (l *Log) wake() {
	select {
	case l.ready <- struct{}{}:
	default: // a signal is waiting already
	}
}

// Upload sends the log until Close, then returns once the last chunk got through. It runs in its own goroutine.
//
// send uploads one chunk. Upload keeps a chunk until send succeeds, and sends a failed chunk again, unchanged:
// the API stores each seq once, so a repeat is harmless (rule 2). Upload gives up at a refusal, such as
// api.ErrFenced, and when ctx ends.
func (l *Log) Upload(ctx context.Context, send func(ctx context.Context, seq int, text string) error) error {
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	for seq := 0; ; {
		select { // wait for the next second, a full chunk, the end, or a cancel
		case <-tick.C:
		case <-l.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		for { // send everything that is waiting, one chunk at a time, in order
			chunk, closed := l.next()
			if chunk == "" {
				if closed {
					return nil
				}
				break
			}
			if err := retry(ctx, func() error { return send(ctx, seq, chunk) }); err != nil {
				return err
			}
			seq++
		}
	}
}

// next takes the next chunk out of pending: all of it, up to 64 KiB. Longer output is cut after its last newline
// within those 64 KiB, or, without one, at the start of a character. closed says whether more output can come.
func (l *Log) next() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.pending)
	if n > maxChunk {
		n = maxChunk
		if i := bytes.LastIndexByte(l.pending[:n], '\n'); i >= 0 {
			n = i + 1
		} else {
			for !utf8.RuneStart(l.pending[n]) { // pending is valid UTF-8: at most 3 steps back
				n--
			}
		}
	}
	chunk := string(l.pending[:n])
	l.pending = l.pending[n:]
	return chunk, l.closed
}

// retry runs call until it succeeds. After a failure it waits 100 ms, then twice as long each time, up to 2 s
// (contracts/v1.yaml). It gives up at a refusal that a retry can't change, and when ctx ends.
func retry(ctx context.Context, call func() error) error {
	for wait := 100 * time.Millisecond; ; wait = min(2*wait, 2*time.Second) {
		err := call()
		if err == nil || refused(err) {
			return err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// refused tells whether the API refused the call (401, 409 or another 4xx), so that sending it again can't help.
func refused(err error) bool {
	return errors.Is(err, api.ErrUnauthorized) || errors.Is(err, api.ErrFenced) || errors.Is(err, api.ErrRejected)
}
