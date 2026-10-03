package livesession

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// Inbound maps one decoded server frame onto the session stream. A Dialect
// calls its methods from Inbound, under the session lock; the session queues
// the result on its outbox in call order.
type Inbound struct {
	s         *Session
	now       time.Time
	out       []messages.StreamMessage
	interrupt bool
	end       *sessionEnd
}

// sessionEnd is how the frame ended the session: a close reason, and for a
// fatal failure the terminal error reported before SESSION.CLOSE.
type sessionEnd struct {
	reason  string
	failure *messages.ErrorValue
}

// HandleEvent maps one server frame through the dialect. It queues the
// resulting messages on the session outbox, in order with the idle
// watcher's and RESPONSE.CANCEL's, and therefore always returns nil. It
// waits while the reader of Receive is far behind, until the close handshake
// starts.
func (s *Session) HandleEvent(_ context.Context, event models.SessionEvent) []messages.StreamMessage {
	s.mu.Lock()
	in := &Inbound{s: s, now: s.base.Clock().Now()}
	s.dialect.Inbound(event.Data, in)
	s.emitLocked(in.out...)
	if in.end != nil {
		s.endLocked(*in.end)
	}
	s.watchLocked()
	s.mu.Unlock()
	if in.interrupt {
		s.interruptRTCPlayback()
	}
	if in.end != nil {
		s.finishEnd(*in.end)
		return nil
	}
	s.awaitBacklog(outboxHighWater, true)
	return nil
}

// endLocked closes the open segment and utterance, then queues the terminal
// error of a failure and SESSION.CLOSE. Callers hold mu.
func (s *Session) endLocked(end sessionEnd) {
	out, segmentOpen := s.finishLocked()
	s.emitLocked(out...)
	if end.failure != nil {
		// The terminal error is set before the stream reports the end, so a
		// reader that sees SESSION.CLOSE also sees why.
		err := end.failure.Err
		if err == nil {
			err = errors.New(end.failure.Message)
		}
		s.base.SetTerminalError(err)
		s.emitTerminalLocked(messages.StreamMessage{Type: messages.StreamTypeError, Value: end.failure})
		s.emitTerminalLocked(messages.StreamMessage{
			Type: messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValueWithTerminal(s.sessionID, end.reason, end.failure.Classification,
				messages.TerminalReasonTerminalFailure, messages.TerminalProvenanceProvider, outputState(segmentOpen)),
		})
		return
	}
	s.emitTerminalLocked(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: closeValue(s.sessionID, end.reason, segmentOpen)})
}

// finishEnd releases a Close waiting on the handshake once SESSION.CLOSE is
// queued, so ending the session cannot strand it, and lets the reader take
// the end of the stream unless the client is closing and may have stopped
// reading. A fatal failure then ends the session at once: the provider will
// send nothing more that the stream could use.
func (s *Session) finishEnd(end sessionEnd) {
	s.markClosed()
	s.awaitBacklog(0, true)
	if end.failure == nil {
		return
	}
	s.base.CloseWithLog()
}

// Logger returns the session logger, for dialect diagnostics.
func (in *Inbound) Logger() logging.Logger { return in.s.logger() }

// Name returns the session's log prefix.
func (in *Inbound) Name() string { return in.s.name }

func (in *Inbound) emit(msgs ...messages.StreamMessage) { in.out = append(in.out, msgs...) }

// OutputAudio maps one chunk of assistant audio in the session format. It
// opens a segment when none is open; while output is suppressed after a
// cancel it is dropped.
func (in *Inbound) OutputAudio(audio []byte) {
	if len(audio) == 0 {
		return
	}
	out, dropped := in.s.segments.outputAudio(in.now, audio, in.s.mediaType)
	if dropped {
		in.Logger().Debug(in.s.name + ": dropped output audio of a cancelled segment")
	}
	in.emit(out...)
}

// OutputSilence maps one chunk of silent output audio from a transport that
// streams continuously. It joins an open segment's audio and does nothing
// else: silence neither opens nor extends a segment.
func (in *Inbound) OutputSilence(audio []byte) {
	if len(audio) == 0 {
		return
	}
	in.emit(in.s.segments.outputSilence(audio, in.s.mediaType)...)
}

// OutputTranscript maps one assistant transcript fragment.
func (in *Inbound) OutputTranscript(fragment Transcript) {
	in.emit(in.s.segments.outputTranscript(in.now, fragment)...)
	in.emit(in.s.delegations.transcript(messages.RoleAssistant, fragment)...)
}

// InputTranscript maps one user transcript fragment. It never touches the
// open segment: full-duplex overlap is not an interruption.
func (in *Inbound) InputTranscript(fragment Transcript) {
	in.emit(in.s.segments.inputTranscript(in.now, fragment)...)
	in.emit(in.s.delegations.transcript(messages.RoleUser, fragment)...)
}

// Delegation reports a client delegation as DELEGATION.CREATED, with no
// ResponseID, once a user transcript covering offsetMS has arrived or the
// settle window passes (or at the session end), with a snapshot of the
// recent transcript. task is the provider's task text, when it sends one. A
// delegation never touches the open segment and never becomes a tool call.
func (in *Inbound) Delegation(id string, offsetMS int64, task string) {
	in.emit(in.s.delegations.created(in.now, id, offsetMS, task)...)
}

// EndSegment closes the open segment as completed, at a provider's explicit
// end of the assistant turn.
func (in *Inbound) EndSegment() {
	in.emit(in.s.segments.endSegment(segmentStatusCompleted)...)
}

// InterruptSegment reports that the provider dropped the open segment's
// queued audio (a server-side barge-in): the segment ends cancelled with
// partial output and local playback is interrupted. The provider already
// stopped, so later output opens a new segment and nothing is suppressed.
func (in *Inbound) InterruptSegment() {
	in.emit(in.s.segments.endSegment(segmentStatusCancelled)...)
	in.interrupt = true
}

// EndUtterance closes the open user utterance at a provider's explicit end
// of the user turn. A non-empty final transcript is the utterance's text.
func (in *Inbound) EndUtterance(final string) {
	in.emit(in.s.segments.endUtterance(in.now, final)...)
}

// SessionUpdated reports a configuration change of session id.
func (in *Inbound) SessionUpdated(id string) {
	in.emit(messages.StreamMessage{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue(id)})
}

// Error reports a non-terminal command error.
func (in *Inbound) Error(value *messages.ErrorValue) {
	in.emit(messages.StreamMessage{Type: messages.StreamTypeError, Value: value})
}

// Closed reports the end of the session with a close reason (see the
// CloseReason constants). The open segment and utterance close first.
func (in *Inbound) Closed(reason string) {
	if in.end == nil {
		in.end = &sessionEnd{reason: reason}
	}
}

// Fail ends the session on a fatal provider failure: the open segment and
// utterance close, failure is reported as a terminal ERROR, SESSION.CLOSE
// reports terminal_failure with reason, and the transport closes.
func (in *Inbound) Fail(reason string, failure *messages.ErrorValue) {
	if in.end == nil {
		in.end = &sessionEnd{reason: reason, failure: failure}
	}
}

// closeValue maps a close reason to SESSION.CLOSE.
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

// CommandError builds the stream value of a provider error event. After
// startup a command error does not necessarily end the session, so it is a
// non-terminal diagnostic; a session that does end reports it with its close.
func CommandError(message, errorType, code, param, clientEventID string) *messages.ErrorValue {
	if message == "" {
		message = "openai live error"
	}
	value := messages.NewNonTerminalErrorValueWithDetails(message, errorType, code, param, clientEventID)
	value.Classification = providers.ErrorClassInvalidRequest
	value.TerminalProvenance = messages.TerminalProvenanceProvider
	return value
}
