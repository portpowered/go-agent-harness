package providers

import (
	"context"
	"errors"
	"sync"
)

// OutboundWireDrain tracks provider events admitted to a client-to-provider
// queue until their transport writes settle. The zero value is ready for use.
// It is independent of the queue implementation so providers can use it for
// both drop-on-full and backpressured input paths.
type OutboundWireDrain struct {
	mu      sync.Mutex
	pending int
	idle    chan struct{}
}

// Begin records an event before it is placed on the provider queue. Recording
// first closes the race where a writer consumes a newly queued event before
// the producer has returned from admission.
func (d *OutboundWireDrain) Begin() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.pending == 0 {
		d.idle = make(chan struct{})
	}
	d.pending++
	d.mu.Unlock()
}

// Complete marks an event settled after its transport write returns. Failed
// queue admission must call Complete as well because no writer will observe
// that event.
func (d *OutboundWireDrain) Complete() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.pending > 0 {
		d.pending--
		if d.pending == 0 && d.idle != nil {
			close(d.idle)
		}
	}
	d.mu.Unlock()
}

// Flush waits for all events admitted before the call to settle. The provider
// terminal error is checked after the drain so a failed wire write is not
// mistaken for successful delivery merely because the writer released its
// pending slot.
func (d *OutboundWireDrain) Flush(ctx context.Context, done <-chan struct{}, terminalError func() error) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	if d.pending == 0 {
		d.mu.Unlock()
		return currentTerminalError(terminalError)
	}
	idle := d.idle
	d.mu.Unlock()

	select {
	case <-idle:
		return currentTerminalError(terminalError)
	case <-done:
		// A clean provider close can race the final writer completion. Recheck
		// the counter before classifying the barrier as a failed delivery.
		d.mu.Lock()
		pending := d.pending
		d.mu.Unlock()
		if pending == 0 {
			return currentTerminalError(terminalError)
		}
		if err := currentTerminalError(terminalError); err != nil {
			return err
		}
		return errors.New("provider session closed before outbound wire drain")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func currentTerminalError(terminalError func() error) error {
	if terminalError == nil {
		return nil
	}
	return terminalError()
}
