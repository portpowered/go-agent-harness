//go:build stress

package stress

import (
	"bytes"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// inferenceEntry is the minimal package-local response fixture needed by the
// stress-only inferencer. The ordinary media harness remains shared through
// internal/support; stress keeps its own concurrency-oriented double isolated.
type inferenceEntry struct {
	result messages.InferenceResult
	chunks []string
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	// changed is closed and cleared by the next Write so waiters block on a
	// signal instead of polling.
	changed chan struct{}
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.changed != nil {
		close(b.changed)
		b.changed = nil
	}
	return n, err
}

// waitFor blocks until the buffer contains want or timeout elapses, and
// reports whether want appeared.
func (b *safeBuffer) waitFor(want string, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		text := b.buf.String()
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		changed := b.changed
		b.mu.Unlock()
		if strings.Contains(text, want) {
			return true
		}
		select {
		case <-changed:
		case <-deadline.C:
			return false
		}
	}
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// settleGoroutines waits until at most limit goroutines run or timeout
// elapses, and returns the last count. Goroutine exit publishes no signal,
// so the count is resampled on a ticker; the wait ends as soon as the
// count is within the limit.
func settleGoroutines(limit int, timeout time.Duration) int {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	resample := time.NewTicker(settleResampleInterval)
	defer resample.Stop()
	for {
		count := runtime.NumGoroutine()
		if count <= limit {
			return count
		}
		select {
		case <-resample.C:
		case <-deadline.C:
			return runtime.NumGoroutine()
		}
	}
}

// settleResampleInterval is how often settleGoroutines resamples the count.
const settleResampleInterval = 10 * time.Millisecond
