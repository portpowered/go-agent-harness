package openaichatgpt

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout is how long a response stream may send nothing
// before the turn fails: Codex's DEFAULT_STREAM_IDLE_TIMEOUT_MS
// (codex-rs/model-provider-info/src/lib.rs).
const DefaultStreamIdleTimeout = 300 * time.Second

// idleBody closes its body when no byte arrives for the timeout, which
// unblocks a pending Read with an error.
type idleBody struct {
	body     io.Reader
	close    func() error
	activity chan struct{}
	done     chan struct{}
	stop     sync.Once
	idle     atomic.Bool
}

// watchIdle starts watching body. A non-positive timeout disables the watch.
func watchIdle(body io.Reader, closeBody func() error, timeout time.Duration) *idleBody {
	b := &idleBody{body: body, close: closeBody, activity: make(chan struct{}, 1), done: make(chan struct{})}
	if timeout > 0 {
		go b.watch(timeout)
	}
	return b
}

func (b *idleBody) watch(timeout time.Duration) {
	for {
		timer := time.NewTimer(timeout)
		select {
		case <-b.activity:
			timer.Stop()
		case <-b.done:
			timer.Stop()
			return
		case <-timer.C:
			b.idle.Store(true)
			if err := b.close(); err != nil {
				return
			}
			return
		}
	}
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		select {
		case b.activity <- struct{}{}:
		default:
		}
	}
	return n, err
}

// idled reports whether the watch closed the body.
func (b *idleBody) idled() bool { return b.idle.Load() }

// Close stops the watch and closes the body.
func (b *idleBody) Close() error {
	b.stop.Do(func() { close(b.done) })
	if b.idled() {
		return nil
	}
	return b.close()
}
