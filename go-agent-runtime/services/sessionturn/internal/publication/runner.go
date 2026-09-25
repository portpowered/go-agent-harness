package publication

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	phaseSessionReady = "session_ready"
	phaseBrokerEvent  = "broker_event"
)

// runner is the single goroutine that serializes readiness, events, the
// settle boundary, and refreshes for one publisher.
type runner struct {
	p              *Publisher
	events         <-chan sessionturn.BrowserEvent
	ready          <-chan struct{}
	settle         sessionturn.Timer
	settleC        <-chan time.Time
	pendingRefresh bool
}

func newRunner(p *Publisher, events <-chan sessionturn.BrowserEvent) *runner {
	return &runner{p: p, events: events, ready: p.ready}
}

func (r *runner) run(ctx context.Context) {
	defer close(r.p.done)
	defer r.stopSettle()
	for {
		if !r.step(ctx) {
			return
		}
	}
}

// step handles one wake-up and reports whether the loop continues.
func (r *runner) step(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		r.p.setLifecycle(sessionturn.PublicationStopped)
		return false
	case <-r.ready:
		return r.onReady(ctx)
	case event, ok := <-r.events:
		return r.onEvent(event, ok)
	case <-r.settleC:
		return r.onSettle(ctx)
	}
}

// onReady drains events queued during the provider handshake so the first
// publication observes the latest catalog state.
func (r *runner) onReady(ctx context.Context) bool {
	r.ready = nil
	r.p.setLifecycle(sessionturn.PublicationReady)
	if !r.drain() {
		return false
	}
	r.pendingRefresh = false
	return r.p.refreshAndPublish(ctx, phaseSessionReady) == nil
}

func (r *runner) onEvent(event sessionturn.BrowserEvent, ok bool) bool {
	if !ok {
		r.p.setLifecycle(sessionturn.PublicationWatchClosed)
		return false
	}
	if !r.p.consumeEvent(event) || r.ready != nil {
		return true
	}
	r.pendingRefresh = true
	r.resetSettle()
	return true
}

// onSettle folds a buffered burst in before reading the catalog, so related
// selection, generation, and catalog events publish only once.
func (r *runner) onSettle(ctx context.Context) bool {
	r.stopSettle()
	if !r.pendingRefresh {
		return true
	}
	if !r.drain() {
		return false
	}
	r.pendingRefresh = false
	return r.p.refreshAndPublish(ctx, phaseBrokerEvent) == nil
}

// drain consumes every already queued event without blocking. It reports
// false when the watch closed.
func (r *runner) drain() bool {
	for {
		select {
		case event, ok := <-r.events:
			if !ok {
				r.p.setLifecycle(sessionturn.PublicationWatchClosed)
				return false
			}
			if r.p.consumeEvent(event) {
				r.pendingRefresh = true
			}
		default:
			return true
		}
	}
}

func (r *runner) resetSettle() {
	if r.settle == nil {
		r.settle = r.p.timers.NewTimer(sessionturn.PublicationSettleWindow)
	} else {
		stopTimer(r.settle)
		r.settle.Reset(sessionturn.PublicationSettleWindow)
	}
	r.settleC = r.settle.C()
}

func (r *runner) stopSettle() {
	if r.settle == nil {
		return
	}
	stopTimer(r.settle)
	r.settleC = nil
}

func stopTimer(timer sessionturn.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
}

type wallTimers struct{}

func (wallTimers) NewTimer(duration time.Duration) sessionturn.Timer {
	return wallTimer{timer: time.NewTimer(duration)}
}

type wallTimer struct{ timer *time.Timer }

func (t wallTimer) C() <-chan time.Time               { return t.timer.C }
func (t wallTimer) Stop() bool                        { return t.timer.Stop() }
func (t wallTimer) Reset(duration time.Duration) bool { return t.timer.Reset(duration) }
