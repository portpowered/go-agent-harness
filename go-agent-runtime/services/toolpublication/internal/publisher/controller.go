package publisher

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
)

type controllerState struct {
	ready          <-chan struct{}
	settleTimer    toolpublication.Timer
	settleC        <-chan time.Time
	pendingRefresh bool
}

func (p *publisher) run(ctx context.Context, events <-chan toolpublication.Event) {
	defer close(p.done)
	state := controllerState{ready: p.ready}
	for {
		select {
		case <-ctx.Done():
			p.setLifecycle(toolpublication.LifecycleStopped)
			return
		case <-state.ready:
			state.ready = nil
			if !p.handleReady(ctx, events, &state) {
				return
			}
		case event, ok := <-events:
			if !p.handleEvent(event, ok, &state) {
				return
			}
		case <-state.settleC:
			if !p.handleSettled(ctx, events, &state) {
				return
			}
		}
	}
}

func (p *publisher) handleReady(ctx context.Context, events <-chan toolpublication.Event, state *controllerState) bool {
	p.setLifecycle(toolpublication.LifecycleReady)
	if !p.drainEvents(events, state) {
		return false
	}
	state.pendingRefresh = false
	return p.refreshAndPublish(ctx, "session_ready") == nil
}

func (p *publisher) handleEvent(event toolpublication.Event, ok bool, state *controllerState) bool {
	if !ok {
		p.setLifecycle(toolpublication.LifecycleWatchGone)
		return false
	}
	if !p.consumeEvent(event) || state.ready != nil {
		return true
	}
	state.pendingRefresh = true
	return p.resetSettleTimer(state)
}

func (p *publisher) handleSettled(ctx context.Context, events <-chan toolpublication.Event, state *controllerState) bool {
	p.stopSettleTimer(state)
	if !state.pendingRefresh {
		return true
	}
	if !p.drainEvents(events, state) {
		return false
	}
	state.pendingRefresh = false
	return p.refreshAndPublish(ctx, "broker_event") == nil
}

func (p *publisher) drainEvents(events <-chan toolpublication.Event, state *controllerState) bool {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				p.setLifecycle(toolpublication.LifecycleWatchGone)
				return false
			}
			if p.consumeEvent(event) {
				state.pendingRefresh = true
			}
		default:
			return true
		}
	}
}

func (p *publisher) resetSettleTimer(state *controllerState) bool {
	if state.settleTimer == nil {
		state.settleTimer = p.timerFactory.NewTimer(toolpublication.SettleWindow)
		if state.settleTimer == nil {
			if err := p.fail("timer", p.latestEventSequence(), errors.New("timer factory returned a nil timer")); err != nil {
				return false
			}
			return false
		}
	} else {
		if !state.settleTimer.Stop() {
			select {
			case <-state.settleTimer.C():
			default:
			}
		}
		state.settleTimer.Reset(toolpublication.SettleWindow)
	}
	state.settleC = state.settleTimer.C()
	return true
}

func (p *publisher) stopSettleTimer(state *controllerState) {
	if state.settleTimer == nil {
		return
	}
	if !state.settleTimer.Stop() {
		select {
		case <-state.settleTimer.C():
		default:
		}
	}
	state.settleC = nil
}

func (p *publisher) consumeEvent(event toolpublication.Event) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Sequence != 0 && event.Sequence <= p.state.LatestEventSequence {
		return false
	}
	if event.Sequence > p.state.LatestEventSequence {
		p.state.LatestEventSequence = event.Sequence
	}
	if !isPublicationEvent(event.Type) {
		return false
	}
	candidate := publicationEvent{browserID: event.BrowserID, targetID: event.TargetID, generation: event.Generation, sequence: event.Sequence}
	if staleGeneration(candidate, p.pending, p.hasPending) || staleGeneration(candidate, publicationEvent{
		browserID:  p.state.LastSuccessfulBrowserID,
		targetID:   p.state.LastSuccessfulTargetID,
		generation: p.state.LastSuccessfulGeneration,
	}, p.state.LastSuccessfulGeneration != 0) {
		return false
	}
	candidate.generation = inferredGeneration(candidate, p.pending, p.hasPending, publicationEvent{
		browserID:  p.state.LastSuccessfulBrowserID,
		targetID:   p.state.LastSuccessfulTargetID,
		generation: p.state.LastSuccessfulGeneration,
	})
	p.pending = candidate
	p.hasPending = true
	return true
}

func isPublicationEvent(eventType toolpublication.EventType) bool {
	switch eventType {
	case toolpublication.EventSelected, toolpublication.EventCatalogChanged, toolpublication.EventGenerationChanged:
		return true
	default:
		return false
	}
}

func staleGeneration(candidate, previous publicationEvent, previousPresent bool) bool {
	return previousPresent && sameTarget(candidate, previous) && candidate.generation != 0 && previous.generation != 0 && candidate.generation < previous.generation
}

func inferredGeneration(candidate, pending publicationEvent, hasPending bool, successful publicationEvent) uint64 {
	if candidate.generation != 0 {
		return candidate.generation
	}
	if hasPending && sameTarget(candidate, pending) {
		return pending.generation
	}
	if sameTarget(candidate, successful) {
		return successful.generation
	}
	return 0
}
