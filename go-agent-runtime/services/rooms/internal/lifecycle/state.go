package lifecycle

import (
	"context"
	"errors"
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
	participant    rooms.Participant
	handle         session.LiveHandle
	endpoints      audio.MediaEndpoints
	media          rooms.MediaPorts
	bridge         *mediaBridge
	events         *eventDrain
	finished       chan struct{}
	responseMu     sync.Mutex
	responseEnd    chan struct{}
	responseActive bool
	boundReason    rooms.RoomTerminationReason
	boundActive    bool
	boundCancelled bool
	boundCompleted bool
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
	mu            sync.Mutex
	active        []*activeParticipant
	results       map[string]rooms.RoomParticipantResult
	terminals     map[string]terminalMetadata
	turns         map[string]int
	agentCount    int
	agentIDs      map[string]struct{}
	turnsBound    int
	boundReason   rooms.RoomTerminationReason
	boundCause    error
	boundGrace    time.Duration
	boundSettle   bool
	boundDoneCh   chan struct{}
	boundDoneOnce sync.Once
	wait          func(context.Context, time.Duration) error
	failure       rooms.FailureService
	stop          context.CancelCauseFunc
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
	wrapped := s.participantFailure(id, err)
	s.mu.Lock()
	boundGrace := s.boundSettle
	s.mu.Unlock()
	if closeErr := value.closeMedia(); closeErr != nil {
		wrapped = errors.Join(wrapped, closeErr)
	}
	if value.handle != nil {
		value.handle.Cancel(wrapped)
	}
	if boundGrace && !isParticipantIsolatedFault(err) {
		s.setFailure(wrapped)
	}
}

func (s *runState) participantFailure(id string, err error) error {
	if s != nil && s.failure != nil {
		return s.failure.ParticipantFailure(rooms.ParticipantFailureRequest{ParticipantID: id, Cause: err})
	}
	return err
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
			s.finishActive(value, reason, err)
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
		// Close joins the provider and closes its stream; drain it before waiting
		// to preserve critical liveness events.
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
	s.finishProjection(participant, reason, err, nil)
}

func (s *runState) finishActive(active *activeParticipant, reason rooms.ParticipantTerminationReason, err error) {
	if active == nil {
		return
	}
	s.finishProjection(active.participant, reason, err, active)
}

func (s *runState) finishProjection(participant rooms.Participant, reason rooms.ParticipantTerminationReason, err error, active *activeParticipant) {
	if reason == "" {
		reason = rooms.ParticipantTerminationEnded
	}
	value := rooms.RoomParticipantResult{
		ID: participant.ID, ParticipantID: participant.ID, Reason: reason, TerminationReason: reason,
		TerminationTrigger: string(reason), Connected: reason != rooms.ParticipantTerminationError, Error: s.errorString(err),
		TurnsCompleted: s.turnCount(participant.ID),
	}
	s.mu.Lock()
	if metadata, ok := s.terminals[participant.ID]; ok {
		value.Classification = metadata.classification
		value.TerminalReason = metadata.reason
		value.TerminalProvenance = metadata.provenance
		value.OutputState = metadata.outputState
	}
	if active != nil && reason != rooms.ParticipantTerminationError {
		boundReason, wasActive, cancelled, completed := active.boundState()
		if boundReason != "" {
			value.TerminationTrigger = string(boundReason)
			value.TerminationDisposition = "completed"
			if wasActive {
				value.TerminationTrigger += "_mid_response"
				if cancelled {
					value.TerminationDisposition = "cancelled_after_grace"
					value.Classification = rooms.RoomBoundCancelledClassification
					value.TerminalReason = string(messages.TerminalReasonCancellation)
					value.TerminalProvenance = string(messages.TerminalProvenanceRoom)
					value.OutputState = string(messages.TerminalOutputNone)
				} else if completed {
					value.TerminationDisposition = "completed_during_grace"
				}
			}
		}
	}
	if _, exists := s.results[participant.ID]; !exists {
		s.results[participant.ID] = value
	}
	s.mu.Unlock()
	s.stopWhenAgentsDone()
}

func (s *runState) errorString(err error) string {
	if err == nil || !participantWaitFailed(err) {
		return ""
	}
	if s != nil && s.failure != nil {
		return s.failure.Sanitize(err, nil)
	}
	return err.Error()
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
