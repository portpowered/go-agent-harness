package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const boundResponseSettleTimeout = 50 * time.Millisecond

func (p *activeParticipant) observeEvent(event session.LiveEvent) {
	if p == nil {
		return
	}
	message := event.Message
	if message == nil {
		if isTerminalEvent(event) {
			p.finishResponse()
		}
		return
	}
	//nolint:exhaustive // only response-boundary events affect bound shutdown.
	switch message.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		if message.Role != messages.RoleTool {
			p.startResponse()
		}
	case messages.StreamTypeMessageEnd, messages.StreamTypeLoopEnd, messages.StreamTypeSessionClose, messages.StreamTypeError:
		if message.Role != messages.RoleTool {
			p.finishResponse()
		}
	default:
		return
	}
}

func (p *activeParticipant) startResponse() {
	p.responseMu.Lock()
	if !p.responseActive {
		p.responseActive = true
		p.responseEnd = make(chan struct{})
	}
	p.responseMu.Unlock()
}

func (p *activeParticipant) finishResponse() {
	p.responseMu.Lock()
	if p.responseActive {
		p.responseActive = false
		close(p.responseEnd)
	}
	p.responseMu.Unlock()
}

func (p *activeParticipant) responseState() (bool, <-chan struct{}) {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	return p.responseActive, p.responseEnd
}

func (p *activeParticipant) markBound(reason rooms.RoomTerminationReason) {
	p.responseMu.Lock()
	p.boundReason, p.boundActive = reason, p.responseActive
	p.responseMu.Unlock()
}

func (p *activeParticipant) markBoundCompleted() {
	p.responseMu.Lock()
	p.boundCompleted = true
	p.responseMu.Unlock()
}

func (p *activeParticipant) cancelBoundResponse() error {
	p.responseMu.Lock()
	if !p.boundActive || p.boundCancelled || p.handle == nil {
		p.responseMu.Unlock()
		return nil
	}
	p.boundCancelled = true
	p.responseMu.Unlock()
	cancelContext, cancel := context.WithTimeout(context.Background(), participantResponseCancelTimeout)
	defer cancel()
	return p.handle.Send(cancelContext, session.LiveControl{Kind: session.LiveControlResponseCancel})
}

func (p *activeParticipant) boundState() (rooms.RoomTerminationReason, bool, bool, bool) {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	return p.boundReason, p.boundActive, p.boundCancelled, p.boundCompleted
}

func (p *activeParticipant) waitResponseSettled(ctx context.Context) {
	active, done := p.responseState()
	if !active || done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *runState) setFailure(err error) {
	if s == nil || err == nil {
		return
	}
	var stop context.CancelCauseFunc
	var boundDone bool
	s.mu.Lock()
	if s.boundReason == "" {
		s.boundReason, s.boundCause = rooms.RoomTerminationFailed, err
		stop = s.stop
	} else if s.boundSettle && isBoundReason(s.boundReason) {
		// A provider failure observed while a room bound is draining is more
		// authoritative than the bound cancellation.
		s.boundReason, s.boundCause, s.boundSettle = rooms.RoomTerminationFailed, err, false
		stop, boundDone = s.stop, true
	}
	s.mu.Unlock()
	if boundDone {
		s.closeBound()
	}
	if stop != nil {
		stop(err)
	}
}

func (s *runState) noteTurn(id string) {
	s.mu.Lock()
	s.turns[id]++
	bound := s.turnsBound > 0 && s.boundReason == "" && allAgentsReachedBound(s.turns, s.agentCount, s.turnsBound)
	s.mu.Unlock()
	if bound {
		s.setBound(rooms.RoomTerminationMaxTurnsReached, errTurnsBound)
	}
}

func allAgentsReachedBound(turns map[string]int, agentCount, bound int) bool {
	if agentCount == 0 || len(turns) < agentCount {
		return false
	}
	for _, count := range turns {
		if count < bound {
			return false
		}
	}
	return true
}

func (s *runState) setBound(reason rooms.RoomTerminationReason, cause error) {
	if !isBoundReason(reason) {
		s.setTerminal(reason, cause)
		return
	}
	s.mu.Lock()
	if s.boundReason != "" {
		s.mu.Unlock()
		return
	}
	s.boundReason, s.boundCause, s.boundSettle = reason, cause, true
	active, grace := append([]*activeParticipant(nil), s.active...), s.boundGrace
	s.mu.Unlock()
	for _, participant := range active {
		if participant != nil {
			participant.markBound(reason)
		}
	}
	go s.finishBound(active, reason, cause, grace)
}

func (s *runState) setTerminal(reason rooms.RoomTerminationReason, cause error) {
	var stop context.CancelCauseFunc
	s.mu.Lock()
	if s.boundReason == "" {
		s.boundReason, s.boundCause, stop = reason, cause, s.stop
	}
	s.mu.Unlock()
	if stop != nil {
		stop(cause)
	}
}

func isBoundReason(reason rooms.RoomTerminationReason) bool {
	return reason == rooms.RoomTerminationMaxTurnsReached || reason == rooms.RoomTerminationMaxDurationReached
}

func (s *runState) finishBound(active []*activeParticipant, reason rooms.RoomTerminationReason, cause error, grace time.Duration) {
	if !hasBoundActiveResponse(active) {
		s.releaseBound(reason, cause)
		return
	}
	if !s.waitBoundGrace(grace) || !s.boundSettlingFor(reason) {
		return
	}
	if err := s.cancelBoundResponses(active); err != nil {
		s.setFailure(err)
		return
	}
	s.waitBoundResponses(active)
	s.releaseBound(reason, cause)
}

func hasBoundActiveResponse(active []*activeParticipant) bool {
	for _, participant := range active {
		if participant != nil && participant.boundWasActive() {
			return true
		}
	}
	return false
}

func (s *runState) waitBoundGrace(grace time.Duration) bool {
	if grace <= 0 {
		return !s.boundDone()
	}
	waitDone := make(chan struct{})
	waitContext, cancelWait := context.WithCancel(context.Background())
	go func() {
		if s.wait != nil {
			if err := s.wait(waitContext, grace); err != nil && !errors.Is(err, context.Canceled) {
				s.setFailure(err)
			}
		} else {
			timer := time.NewTimer(grace)
			select {
			case <-timer.C:
			case <-waitContext.Done():
				timer.Stop()
			}
		}
		close(waitDone)
	}()
	select {
	case <-waitDone:
		cancelWait()
		return true
	case <-s.boundDoneCh:
		cancelWait()
		return false
	}
}

func (s *runState) cancelBoundResponses(active []*activeParticipant) error {
	for _, participant := range active {
		if participant == nil || !participant.boundWasActive() {
			continue
		}
		if !participant.responseIsActive() {
			participant.markBoundCompleted()
			continue
		}
		if err := participant.cancelBoundResponse(); err != nil {
			return fmt.Errorf("cancel bound response for %q: %w", participant.participant.ID, err)
		}
	}
	return nil
}

func (p *activeParticipant) boundWasActive() bool {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	return p.boundActive
}

func (p *activeParticipant) responseIsActive() bool {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	return p.responseActive
}

func (s *runState) waitBoundResponses(active []*activeParticipant) {
	waitContext, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	settled := make(chan struct{})
	go waitBoundParticipants(waitContext, active, settled)
	timeout := make(chan struct{})
	go s.waitBoundResponseTimeout(waitContext, timeout)
	select {
	case <-settled:
	case <-timeout:
	case <-s.boundDoneCh:
	}
}

func waitBoundParticipants(ctx context.Context, active []*activeParticipant, settled chan<- struct{}) {
	for _, participant := range active {
		if participant != nil && participant.boundWasActive() && participant.boundWasCancelled() {
			participant.waitResponseSettled(ctx)
		}
	}
	close(settled)
}

func (s *runState) waitBoundResponseTimeout(ctx context.Context, timeout chan<- struct{}) {
	defer close(timeout)
	if s.wait != nil {
		if err := s.wait(ctx, boundResponseSettleTimeout); err != nil && !errors.Is(err, context.Canceled) {
			s.setFailure(err)
		}
		return
	}
	timer := time.NewTimer(boundResponseSettleTimeout)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
	}
}

func (p *activeParticipant) boundWasCancelled() bool {
	p.responseMu.Lock()
	defer p.responseMu.Unlock()
	return p.boundCancelled
}

func (s *runState) boundSettlingFor(reason rooms.RoomTerminationReason) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundSettle && s.boundReason == reason
}

func (s *runState) boundDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.boundSettle
}

func (s *runState) releaseBound(reason rooms.RoomTerminationReason, cause error) {
	var stop context.CancelCauseFunc
	var done bool
	s.mu.Lock()
	if s.boundSettle && s.boundReason == reason {
		s.boundSettle, stop, done = false, s.stop, true
	}
	s.mu.Unlock()
	if done {
		s.closeBound()
	}
	if stop != nil {
		stop(cause)
	}
}

func (s *runState) closeBound() {
	if s == nil || s.boundDoneCh == nil {
		return
	}
	s.boundDoneOnce.Do(func() { close(s.boundDoneCh) })
}

func isParticipantIsolatedFault(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, silentProviderEmptyResponse) || strings.Contains(message, silentProviderTimeout)
}
