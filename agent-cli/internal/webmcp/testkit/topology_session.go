package testkit

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func (s *ScriptedTargetSession) emitPublished(event webmcp.BrowserEvent) (PublishedEvent, error) {
	if s == nil {
		return PublishedEvent{}, webmcp.ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return PublishedEvent{}, webmcp.ErrClosed
	}
	return s.emitPublishedLocked(event, false)
}

// BlockEnableWebMCP holds the enable operation after admission. Disconnect
// also releases it, which makes selection-loss races deterministic.
func (s *ScriptedTargetSession) BlockEnableWebMCP() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.enableBlocked = true
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) UnblockEnableWebMCP() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.enableBlocked {
		s.enableBlocked = false
		close(s.enableChanges)
		s.enableChanges = make(chan struct{})
	}
	s.mu.Unlock()
}

// WaitForInvocationAdmission names the operation-admitted point already
// exposed by WaitForInvocation.
func (s *ScriptedTargetSession) WaitForInvocationAdmission(ctx context.Context) (InvocationRecord, error) {
	return s.WaitForInvocation(ctx)
}

// WaitForTerminalObservation waits until the target has published a terminal
// response or lifecycle outcome for id.
func (s *ScriptedTargetSession) WaitForTerminalObservation(ctx context.Context, id webmcp.InvocationID) (TerminalObservation, error) {
	if s == nil {
		return TerminalObservation{}, webmcp.ErrClosed
	}
	if id == "" {
		return TerminalObservation{}, fmt.Errorf("%w: empty invocation ID", webmcp.ErrInvocationNotFound)
	}
	ctx = nonNilContext(ctx)
	for {
		s.mu.Lock()
		if observation, ok := s.terminalObserved[id]; ok {
			result := cloneTerminalObservation(observation)
			s.mu.Unlock()
			return result, nil
		}
		if _, ok := s.invokes[id]; !ok {
			s.mu.Unlock()
			return TerminalObservation{}, fmt.Errorf("%w: %s", webmcp.ErrInvocationNotFound, id)
		}
		changes := s.terminalChanges
		done := s.done
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return TerminalObservation{}, ctx.Err()
		case <-changes:
		case <-done:
			// terminate marks all unresolved records before closing done. A
			// second pass observes that lifecycle publication; if publication
			// failed, return the session's terminal error instead of hanging.
			if err := s.Err(); err != nil {
				return TerminalObservation{}, err
			}
		}
	}
}

// WaitForTerminal is the concise terminal-observed barrier.
func (s *ScriptedTargetSession) WaitForTerminal(ctx context.Context, id webmcp.InvocationID) (InvocationRecord, error) {
	observation, err := s.WaitForTerminalObservation(ctx, id)
	if err != nil {
		return InvocationRecord{}, err
	}
	return cloneInvocationRecord(observation.Invocation), nil
}

// TerminalObservations returns a bounded defensive snapshot of terminal
// outcomes observed by this session.
func (s *ScriptedTargetSession) TerminalObservations() []TerminalObservation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]TerminalObservation, len(s.terminalHistory))
	for i, observation := range s.terminalHistory {
		result[i] = cloneTerminalObservation(observation)
	}
	return result
}

func (s *ScriptedTargetSession) markTerminalObserved(id webmcp.InvocationID, publication PublishedEvent) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	if _, exists := s.terminalObserved[id]; exists {
		s.mu.Unlock()
		return
	}
	record, exists := s.invokes[id]
	if !exists || !record.Terminal {
		s.mu.Unlock()
		return
	}
	observation := TerminalObservation{
		Sequence:            publication.Event.Sequence,
		PublicationSequence: publication.Sequence,
		Event:               cloneBrowserEvent(publication.Event),
		Invocation:          cloneInvocationRecord(*record),
	}
	s.terminalObserved[id] = observation
	if len(s.terminalHistory) >= maxTerminalHistory {
		copy(s.terminalHistory, s.terminalHistory[1:])
		s.terminalHistory[len(s.terminalHistory)-1] = observation
	} else {
		s.terminalHistory = append(s.terminalHistory, observation)
	}
	close(s.terminalChanges)
	s.terminalChanges = make(chan struct{})
	s.mu.Unlock()
}

// InjectLateEvent publishes an event produced by this session into the
// current session for the same target/address. The source identity,
// generation, and producer sequence are preserved; the destination's fake
// catalog and invocation state are not mutated.
func (s *ScriptedTargetSession) InjectLateEvent(event webmcp.BrowserEvent) error {
	if s == nil {
		return webmcp.ErrClosed
	}
	destination := s.runtime.lateEventDestination(s)
	if destination == nil {
		return ErrNoActiveTargetSession
	}
	return s.InjectLateEventInto(destination, event)
}

// InjectLateEventInto targets a specific current session, which is useful
// when two browser identities deliberately share a target ID.
func (s *ScriptedTargetSession) InjectLateEventInto(destination *ScriptedTargetSession, event webmcp.BrowserEvent) error {
	if s == nil || destination == nil {
		return ErrNoActiveTargetSession
	}
	produced := s.produceEvent(event)
	return destination.admitInjectedEvent(produced)
}

// EmitLateEvent is an alias for InjectLateEvent.
func (s *ScriptedTargetSession) EmitLateEvent(event webmcp.BrowserEvent) error {
	return s.InjectLateEvent(event)
}

func (s *ScriptedTargetSession) produceEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	s.mu.Lock()
	produced := s.decorateProducedEventLocked(event)
	s.mu.Unlock()
	return produced
}

func (s *ScriptedTargetSession) admitInjectedEvent(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	select {
	case s.events <- cloneBrowserEvent(event):
		s.mu.Unlock()
		s.runtime.publishEvent(event)
		return nil
	default:
		s.mu.Unlock()
		return webmcp.ErrEventBufferFull
	}
}

// NavigateSequence emits each step synchronously in the order supplied.
func (s *ScriptedTargetSession) NavigateSequence(steps ...Navigation) error {
	for _, step := range steps {
		if err := s.Navigate(step.URL, step.Origin); err != nil {
			return err
		}
	}
	return nil
}

// EmitNavigationSequence is an event-oriented alias for NavigateSequence.
func (s *ScriptedTargetSession) EmitNavigationSequence(steps ...Navigation) error {
	return s.NavigateSequence(steps...)
}

// NavigateBurst accepts URL-only navigation steps for compact churn tests.
func (s *ScriptedTargetSession) NavigateBurst(urls ...string) error {
	steps := make([]Navigation, len(urls))
	for i, value := range urls {
		steps[i] = Navigation{URL: value}
	}
	return s.NavigateSequence(steps...)
}

// EmitNavigationBurst is an alias for NavigateBurst.
func (s *ScriptedTargetSession) EmitNavigationBurst(urls ...string) error {
	return s.NavigateBurst(urls...)
}

// CloseTarget explicitly closes the target represented by this session. It
// differs from Close for an external session, whose ordinary close remains a
// detach-only release.
func (s *ScriptedTargetSession) CloseTarget() error {
	if s == nil {
		return webmcp.ErrClosed
	}
	return s.terminateWithOptions(webmcp.EventTargetDetached, "target_closed", webmcp.ErrClosed, true, true)
}

// EmitTargetClosed is a descriptive alias for CloseTarget.
func (s *ScriptedTargetSession) EmitTargetClosed() error { return s.CloseTarget() }

func (s *ScriptedTargetSession) Detach(reason string) error {
	return s.terminate(webmcp.EventTargetDetached, reason, webmcp.ErrClosed)
}

func (s *ScriptedTargetSession) Disconnect(reasons ...string) error {
	reason := ""
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	if reason == "" {
		reason = "browser_disconnected"
	}
	return s.terminate(webmcp.EventBrowserDisconnected, reason, disconnectedError(s.target.BrowserID, s.target.ID, "session", reason))
}

func (s *ScriptedTargetSession) EmitTargetDetached(reason string) error { return s.Detach(reason) }

func (s *ScriptedTargetSession) EmitDisconnected(reasons ...string) error {
	return s.Disconnect(reasons...)
}

func (s *ScriptedTargetSession) decorateProducedEventLocked(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	if event.Sequence == 0 {
		s.sequence++
		event.Sequence = s.sequence
	} else if event.Sequence > s.sequence {
		s.sequence = event.Sequence
	}
	if event.BrowserID == "" {
		event.BrowserID = s.target.BrowserID
	}
	if event.TargetID == "" {
		event.TargetID = s.target.ID
	}
	if event.Generation == 0 {
		event.Generation = s.context.Generation
	}
	for i := range event.Tools {
		if event.Tools[i].BrowserID == "" {
			event.Tools[i].BrowserID = s.target.BrowserID
		}
		if event.Tools[i].TargetID == "" {
			event.Tools[i].TargetID = s.target.ID
		}
		if event.Tools[i].Generation == 0 {
			event.Tools[i].Generation = event.Generation
		}
		if event.Tools[i].Origin == "" {
			event.Tools[i].Origin = s.context.Origin
		}
	}
	return s.runtime.decorateEvent(event, s.target.BrowserID, s.target.ID, s.context.Generation, event.Sequence)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
