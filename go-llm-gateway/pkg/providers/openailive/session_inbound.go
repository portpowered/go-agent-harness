package openailive

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// HandleEvent maps one server frame. It queues the resulting messages on the
// session outbox, in order with the idle watcher's and RESPONSE.CANCEL's, and
// therefore always returns nil. It waits while the reader of Receive is far
// behind, until the close handshake starts.
func (s *liveSession) HandleEvent(ctx context.Context, event models.SessionEvent) []messages.StreamMessage {
	decoded, err := DecodeServerEvent(event.Data)
	if err != nil {
		s.base.Logger().Warn("openai live: undecodable server event", logging.Field{Key: "type", Value: string(event.Type)}, logging.Field{Key: "error", Value: err})
		return nil
	}
	if closed, ok := decoded.(SessionClosed); ok {
		// Mark first, so a Close waiting on the handshake is released even
		// if delivery below waits for a slow reader.
		s.markClosed()
		s.handleClosed(closed)
		return nil
	}
	s.mu.Lock()
	s.emitLocked(s.inboundLocked(decoded)...)
	s.watchLocked()
	s.mu.Unlock()
	s.awaitBacklog(outboxHighWater, true)
	return nil
}

// inboundLocked maps one decoded event other than session.closed. Callers
// hold mu.
func (s *liveSession) inboundLocked(event Event) []messages.StreamMessage {
	now := s.base.Clock().Now()
	switch typed := event.(type) {
	case OutputAudioDelta:
		return s.outputAudioLocked(now, typed)
	case OutputTranscriptDelta:
		return s.segments.outputTranscript(now, TranscriptDelta(typed))
	case InputTranscriptDelta:
		return s.segments.inputTranscript(now, TranscriptDelta(typed))
	case SessionUpdated:
		return []messages.StreamMessage{{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue(typed.Session.ID)}}
	case ErrorEvent:
		return []messages.StreamMessage{{Type: messages.StreamTypeError, Value: commandError(typed)}}
	case UsageUpdated:
		// Usage is a cumulative snapshot; its stream mapping is open (Q7).
		s.base.Logger().Debug("openai live: usage", logging.Field{Key: "seconds", Value: typed.Usage.Seconds})
		return nil
	case DelegationCreated:
		// Delegations reach the harness in a later phase. Until then they
		// are logged and dropped, never turned into tool calls.
		s.base.Logger().Info("openai live: delegation ignored",
			logging.Field{Key: "delegation_id", Value: typed.Delegation.ID}, logging.Field{Key: "target", Value: typed.Delegation.Target})
		return nil
	}
	// Acknowledgements, session.started, info, response.event, transport.*
	// and unknown events carry nothing for the stream.
	s.base.Logger().Debug("openai live: server event not mapped", logging.Field{Key: "type", Value: event.EventType()})
	return nil
}

func (s *liveSession) outputAudioLocked(now time.Time, delta OutputAudioDelta) []messages.StreamMessage {
	audio, err := delta.Bytes()
	if err != nil {
		s.base.Logger().Warn("openai live: undecodable output audio", logging.Field{Key: "error", Value: err})
		return nil
	}
	if len(audio) == 0 {
		return nil
	}
	out, dropped := s.segments.outputAudio(now, audio, s.mediaType)
	if dropped {
		s.base.Logger().Debug("openai live: dropped output audio of a cancelled segment")
	}
	return out
}

// handleClosed closes the open segment and utterance, then reports the
// session end with the close reason mapped onto the existing terminal
// vocabulary. The GPT-Live reason is kept verbatim as Reason.
func (s *liveSession) handleClosed(closed SessionClosed) {
	s.mu.Lock()
	out, segmentOpen := s.segments.finish()
	s.emitLocked(out...)
	s.emitTerminalLocked(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: closeValue(s.sessionID, closed.Reason, segmentOpen)})
	s.mu.Unlock()
	// Let the reader take the end of the stream before the socket closes,
	// unless the client is closing and may have stopped reading.
	s.awaitBacklog(0, true)
}

// closeValue maps a session.closed reason to SESSION.CLOSE.
func closeValue(sessionID, reason string, segmentOpen bool) *messages.SessionCloseValue {
	terminal := messages.TerminalReasonProviderClose
	state := messages.TerminalOutputNotApplicable
	classification := providers.ErrorClassTransport
	switch reason {
	case CloseReasonCloseRequested:
		terminal, classification = messages.TerminalReasonSessionClose, ""
	case CloseReasonExpired, CloseReasonRemoteHangup:
	case CloseReasonContent:
		state = outputState(segmentOpen)
	case CloseReasonConnectionLost:
		terminal, state = messages.TerminalReasonTerminalFailure, outputState(segmentOpen)
	default:
		// A reason the spec does not list is still a provider-side close.
	}
	return messages.NewSessionCloseValueWithTerminal(sessionID, reason, classification, terminal, messages.TerminalProvenanceProvider, state)
}

// commandError maps an error event. After startup a command error does not
// necessarily end the session, so it is a non-terminal diagnostic; a session
// that does end reports it with session.closed.
func commandError(event ErrorEvent) *messages.ErrorValue {
	body := event.Error
	param := ""
	if body.Param != nil {
		param = *body.Param
	}
	message := body.Message
	if message == "" {
		message = "openai live error"
	}
	value := messages.NewNonTerminalErrorValueWithDetails(message, body.Type, body.Code, param, body.ClientEventID)
	value.Classification = providers.ErrorClassInvalidRequest
	value.TerminalProvenance = messages.TerminalProvenanceProvider
	return value
}
