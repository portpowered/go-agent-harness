package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// activeParticipant joins the provider handle, local media ports, and
// invocation-scoped observers. The room runner owns this value until every
// worker has been joined.
type activeParticipant struct {
	participant rooms.Participant
	handle      session.LiveHandle
	endpoints   audio.MediaEndpoints
	media       rooms.MediaPorts
	bridge      *mediaBridge
	events      *eventDrain
	finished    chan struct{}
	// onMediaError retires only this participant when its provider/device edge
	// fails. The room mesh remains available to surviving peers.
	onMediaError func(error)
	// retire removes this participant's routes from the room graph. A provider
	// handle can report a terminal error before its media endpoint observes the
	// close, so lifecycle failures need the same retirement hook as media errors.
	retire     func()
	mediaClose sync.Once
	mediaErr   error
}

// runState is the bounded lifecycle ledger for one room invocation. It keeps
// admission, turn bounds, and terminal results together without exposing
// mutable state through the public room contract.
type runState struct {
	mu          sync.Mutex
	active      []*activeParticipant
	results     map[string]rooms.RoomParticipantResult
	terminals   map[string]terminalMetadata
	turns       map[string]int
	agentCount  int
	agentIDs    map[string]struct{}
	turnsBound  int
	boundReason rooms.RoomTerminationReason
	boundCause  error
	stop        context.CancelCauseFunc
}

type terminalMetadata struct {
	classification string
	reason         string
	provenance     string
	outputState    string
}

func (s *runState) add(active *activeParticipant) {
	s.mu.Lock()
	s.active = append(s.active, active)
	s.mu.Unlock()
}

func (s *runState) noteTerminal(id string, event session.LiveEvent) {
	if s == nil || strings.TrimSpace(id) == "" || (event.Terminal == nil && event.Liveness == nil) {
		return
	}
	value := terminalMetadataFromEvent(event)
	s.mu.Lock()
	if s.terminals == nil {
		s.terminals = make(map[string]terminalMetadata)
	}
	if previous, ok := s.terminals[id]; !ok || previous.classification == "" && value.classification != "" {
		s.terminals[id] = value
	}
	s.mu.Unlock()
}

func terminalMetadataFromEvent(event session.LiveEvent) terminalMetadata {
	value := terminalMetadataFromLiveness(event.Liveness)
	mergeTerminalMetadata(&value, terminalMetadataFromTerminal(event.Terminal))
	return value
}

func terminalMetadataFromLiveness(liveness *session.LiveLivenessFailure) terminalMetadata {
	if liveness == nil {
		return terminalMetadata{}
	}
	return terminalMetadata{
		classification: strings.TrimSpace(liveness.Classification),
		reason:         string(liveness.TerminalReason),
		provenance:     string(liveness.TerminalProvenance),
		outputState:    string(liveness.OutputState),
	}
}

func terminalMetadataFromTerminal(terminal *messages.SessionCloseValue) terminalMetadata {
	if terminal == nil {
		return terminalMetadata{}
	}
	return terminalMetadata{
		classification: strings.TrimSpace(terminal.Classification),
		reason:         string(terminal.TerminalReason),
		provenance:     string(terminal.TerminalProvenance),
		outputState:    string(terminal.OutputState),
	}
}

func mergeTerminalMetadata(destination *terminalMetadata, source terminalMetadata) {
	if destination == nil {
		return
	}
	if destination.classification == "" {
		destination.classification = source.classification
	}
	if destination.reason == "" {
		destination.reason = source.reason
	}
	if destination.provenance == "" {
		destination.provenance = source.provenance
	}
	if destination.outputState == "" {
		destination.outputState = source.outputState
	}
}

func (s *runState) snapshotActive() []*activeParticipant {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*activeParticipant(nil), s.active...)
}

func (s *runState) setFailure(err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	if s.boundReason == "" {
		s.boundReason = rooms.RoomTerminationFailed
		s.boundCause = err
		if s.stop != nil {
			s.stop(err)
		}
	}
	s.mu.Unlock()
}

// failParticipant records a media-plane fault without cancelling the room.
// The participant's own handle and local ports are asked to stop; waitAll
// still performs the single joined cleanup path for its terminal result.
func (s *runState) failParticipant(id string, err error) {
	if s == nil || strings.TrimSpace(id) == "" || err == nil {
		return
	}
	s.mu.Lock()
	var value *activeParticipant
	for _, candidate := range s.active {
		if candidate != nil && candidate.participant.ID == id {
			value = candidate
			break
		}
	}
	s.mu.Unlock()
	if value == nil {
		s.setFailure(err)
		return
	}
	if closeErr := value.closeMedia(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if value.handle != nil {
		value.handle.Cancel(err)
	}
}

func (s *runState) noteTurn(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns[id]++
	if s.turnsBound <= 0 || s.boundReason != "" {
		return
	}
	for _, turns := range s.turns {
		if turns < s.turnsBound {
			return
		}
	}
	if len(s.turns) < s.agentCount {
		return
	}
	s.boundReason = rooms.RoomTerminationMaxTurnsReached
	s.boundCause = errTurnsBound
	if s.stop != nil {
		s.stop(errTurnsBound)
	}
}

func (s *runState) setBound(reason rooms.RoomTerminationReason, cause error) {
	s.mu.Lock()
	if s.boundReason == "" {
		s.boundReason, s.boundCause = reason, cause
		if s.stop != nil {
			s.stop(cause)
		}
	}
	s.mu.Unlock()
}

func (s *runState) waitAll(ctx context.Context, request rooms.RoomRunOptions, now func() time.Time) {
	s.mu.Lock()
	active := append([]*activeParticipant(nil), s.active...)
	s.mu.Unlock()
	var wait sync.WaitGroup
	for _, participant := range active {
		wait.Add(1)
		go func(value *activeParticipant) {
			defer wait.Done()
			err := waitParticipant(ctx, value)
			retireFailedParticipant(value, err)
			reason := participantTerminationReason(err)
			s.finish(value.participant, reason, err)
			if request.OnDiagnostic != nil && err != nil {
				request.OnDiagnostic(value.participant.ID, diagnostic("participant_finished_with_error", err, now))
			}
		}(participant)
	}
	waitDone := make(chan struct{})
	go func() { wait.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-ctx.Done():
		// The per-participant cancellation watchers have already requested
		// shutdown. Keep waiting so Close and Wait ownership is joined before
		// returning to the host.
		<-waitDone
	}
}

func retireFailedParticipant(value *activeParticipant, err error) {
	if value == nil || !participantWaitFailed(err) {
		return
	}
	// A provider can terminate before its media reader observes the closed
	// endpoint. Retire it at the lifecycle boundary so surviving peer routes do
	// not continue targeting a dead participant.
	if value.retire != nil {
		value.retire()
	}
	if value.onMediaError != nil {
		value.onMediaError(err)
	}
}

func participantTerminationReason(err error) rooms.ParticipantTerminationReason {
	if participantWaitFailed(err) {
		return rooms.ParticipantTerminationError
	}
	return rooms.ParticipantTerminationEnded
}

func participantWaitFailed(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errDurationBound) && !errors.Is(err, errTurnsBound)
}

func waitParticipant(ctx context.Context, value *activeParticipant) error {
	if value == nil {
		return nil
	}
	var err error
	if value.handle != nil {
		err = value.handle.Wait()
	} else {
		// Human media workers run until the room context reaches its terminal boundary.
		if ctx != nil {
			<-ctx.Done()
		}
	}
	close(value.finished)
	if value.bridge != nil {
		if stopErr := value.bridge.Stop(); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		if bridgeErr := value.bridge.Wait(); bridgeErr != nil {
			err = errors.Join(err, bridgeErr)
		}
	}
	var closeErr error
	if value.handle != nil {
		closeErr = value.handle.Close()
	}
	mediaErr := value.closeMedia()
	var eventErr error
	if value.events != nil {
		value.events.Stop()
		eventErr = value.events.Wait()
	}
	return errors.Join(err, closeErr, mediaErr, eventErr)
}

func (p *activeParticipant) closeMedia() error {
	if p == nil {
		return nil
	}
	p.mediaClose.Do(func() { p.mediaErr = p.media.Close() })
	return p.mediaErr
}

func (s *runState) finish(participant rooms.Participant, reason rooms.ParticipantTerminationReason, err error) {
	if reason == "" {
		reason = rooms.ParticipantTerminationEnded
	}
	value := rooms.RoomParticipantResult{
		ID: participant.ID, ParticipantID: participant.ID, Reason: reason, TerminationReason: reason,
		TerminationTrigger: string(reason), Connected: err == nil, Error: errorString(err),
		TurnsCompleted: s.turnCount(participant.ID),
	}
	s.mu.Lock()
	if metadata, ok := s.terminals[participant.ID]; ok {
		value.Classification = metadata.classification
		value.TerminalReason = metadata.reason
		value.TerminalProvenance = metadata.provenance
		value.OutputState = metadata.outputState
	}
	if _, exists := s.results[participant.ID]; !exists {
		s.results[participant.ID] = value
	}
	s.mu.Unlock()
	s.stopWhenAgentsDone()
}

const (
	silentProviderEmptyResponse = "silent_provider_empty_response"
	silentProviderTimeout       = "silent_provider_timeout"
)

func terminalLivenessFailure(event session.LiveEvent) error {
	if event.Liveness != nil {
		classification := strings.TrimSpace(event.Liveness.Classification)
		if classification == "" {
			return nil
		}
		if classification == silentProviderEmptyResponse || classification == silentProviderTimeout {
			return fmt.Errorf("%s: provider response produced no observable output", classification)
		}
		return nil
	}
	if event.Terminal == nil {
		return nil
	}
	classification := strings.TrimSpace(event.Terminal.Classification)
	if classification == "" && event.Terminal.TerminalReason == messages.TerminalReasonPartialOutput && event.Terminal.OutputState == messages.TerminalOutputNone {
		classification = silentProviderEmptyResponse
	}
	if classification != silentProviderEmptyResponse && classification != silentProviderTimeout {
		return nil
	}
	return fmt.Errorf("%s: provider response produced no observable output", classification)
}

// stopWhenAgentsDone closes a room that has no provider work left. Human
// participants do not own a LiveHandle, so they need the room cancellation
// signal to release their capture and playback workers after the final agent
// exits naturally without a turn or duration bound.
func (s *runState) stopWhenAgentsDone() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.boundReason != "" || s.agentCount == 0 {
		s.mu.Unlock()
		return
	}
	for id := range s.agentIDs {
		if _, done := s.results[id]; !done {
			s.mu.Unlock()
			return
		}
	}
	s.boundReason = rooms.RoomTerminationStopped
	s.boundCause = nil
	if s.stop != nil {
		s.stop(nil)
	}
	s.mu.Unlock()
}

func (s *runState) turnCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turns[id]
}
