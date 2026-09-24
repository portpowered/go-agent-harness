package agentruntime

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
)

type sessionDynamicToolPublisherRun struct {
	publisher      *sessionDynamicToolPublisher
	loop           *agentloop.AgentLoop
	events         <-chan webmcp.BrokerEvent
	ready          <-chan struct{}
	settleTimer    webmcp.Timer
	settle         <-chan time.Time
	pendingRefresh bool
}

func (p *sessionDynamicToolPublisher) run(ctx context.Context, loop *agentloop.AgentLoop, events <-chan webmcp.BrokerEvent) {
	defer close(p.done)
	run := sessionDynamicToolPublisherRun{publisher: p, loop: loop, events: events, ready: p.ready}
	defer run.stopSettleTimer()
	run.run(ctx)
}

func (r *sessionDynamicToolPublisherRun) run(ctx context.Context) {
	for r.next(ctx) {
	}
}

func (r *sessionDynamicToolPublisherRun) next(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		r.publisher.setLifecycle(SessionDynamicToolPublicationStopped)
		return false
	case <-r.ready:
		return r.onReady(ctx)
	case event, ok := <-r.events:
		return r.onEvent(event, ok)
	case <-r.settle:
		return r.onSettle(ctx)
	}
}

func (r *sessionDynamicToolPublisherRun) onReady(ctx context.Context) bool {
	r.ready = nil
	r.publisher.setLifecycle(SessionDynamicToolPublicationReady)
	if !r.drainEvents() {
		return false
	}
	r.pendingRefresh = false
	return r.refresh(ctx, "session_ready")
}

func (r *sessionDynamicToolPublisherRun) onEvent(event webmcp.BrokerEvent, ok bool) bool {
	if !ok {
		r.publisher.setLifecycle(SessionDynamicToolPublicationWatchGone)
		return false
	}
	if !r.publisher.consumeEvent(event) || r.ready != nil {
		return true
	}
	r.pendingRefresh = true
	r.resetSettleTimer()
	return true
}

func (r *sessionDynamicToolPublisherRun) onSettle(ctx context.Context) bool {
	r.stopSettleTimer()
	if !r.pendingRefresh {
		return true
	}
	if !r.drainEvents() {
		return false
	}
	r.pendingRefresh = false
	return r.refresh(ctx, "broker_event")
}

func (r *sessionDynamicToolPublisherRun) refresh(ctx context.Context, phase string) bool {
	return r.publisher.refreshAndPublish(ctx, r.loop, phase) == nil
}

func (r *sessionDynamicToolPublisherRun) drainEvents() bool {
	for {
		select {
		case event, ok := <-r.events:
			if !ok {
				r.publisher.setLifecycle(SessionDynamicToolPublicationWatchGone)
				return false
			}
			if r.publisher.consumeEvent(event) {
				r.pendingRefresh = true
			}
		default:
			return true
		}
	}
}

func (r *sessionDynamicToolPublisherRun) resetSettleTimer() {
	if r.settleTimer == nil {
		r.settleTimer = r.publisher.timerFactory.NewTimer(sessionDynamicToolPublicationSettleWindow)
	} else {
		if !r.settleTimer.Stop() {
			select {
			case <-r.settleTimer.C():
			default:
			}
		}
		r.settleTimer.Reset(sessionDynamicToolPublicationSettleWindow)
	}
	r.settle = r.settleTimer.C()
}

func (r *sessionDynamicToolPublisherRun) stopSettleTimer() {
	if r.settleTimer == nil {
		return
	}
	if !r.settleTimer.Stop() {
		select {
		case <-r.settleTimer.C():
		default:
		}
	}
	r.settle = nil
}

type sessionDynamicToolPublicationWallTimerFactory struct{}

func (sessionDynamicToolPublicationWallTimerFactory) NewTimer(duration time.Duration) webmcp.Timer {
	return sessionDynamicToolPublicationWallTimer{timer: time.NewTimer(duration)}
}

type sessionDynamicToolPublicationWallTimer struct {
	timer *time.Timer
}

func (t sessionDynamicToolPublicationWallTimer) C() <-chan time.Time { return t.timer.C }

func (t sessionDynamicToolPublicationWallTimer) Stop() bool { return t.timer.Stop() }

func (t sessionDynamicToolPublicationWallTimer) Reset(duration time.Duration) bool {
	return t.timer.Reset(duration)
}
