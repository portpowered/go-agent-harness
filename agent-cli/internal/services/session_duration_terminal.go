package services

import (
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionDurationTerminalState struct {
	admittedInferencer *sessionDurationAdmissionInferencer
	terminalWritten    bool
	responseOutput     bool
	responseComplete   bool
}

func newSessionDurationTerminalState(admittedInferencer *sessionDurationAdmissionInferencer) *sessionDurationTerminalState {
	return &sessionDurationTerminalState{admittedInferencer: admittedInferencer}
}

func (s *sessionDurationTerminalState) observe(msg messages.StreamMessage) {
	if s == nil {
		return
	}
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		s.responseOutput = false
		s.responseComplete = false
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeRefusal:
		s.responseOutput = true
	case messages.StreamTypeTranscriptDelta:
		if msg.Role != messages.RoleUser {
			s.responseOutput = true
		}
	case messages.StreamTypeMessageEnd:
		s.responseComplete = true
	}
}

func (s *sessionDurationTerminalState) outputState() messages.TerminalOutputState {
	if s == nil || !s.responseOutput {
		return messages.TerminalOutputNone
	}
	if s.responseComplete {
		return messages.TerminalOutputComplete
	}
	return messages.TerminalOutputPartial
}

// admitTerminal decides which SESSION.CLOSE messages are visible in the
// normalized duration artifact. The loop emits its own close immediately when
// a close control reaches the coordinator; that close is only a shutdown
// request, not proof that the provider sent a terminal wire event. Defer it
// until the bounded drain has established whether the provider terminal was
// actually observed.
func (s *sessionDurationTerminalState) admitTerminal(planned bool, msg messages.StreamMessage) (messages.StreamMessage, bool) {
	if msg.Type != messages.StreamTypeSessionClose {
		return msg, true
	}
	if s.terminalWritten {
		return msg, false
	}
	if s.admittedInferencer != nil && s.admittedInferencer.isProviderTerminalMessage(msg) {
		s.terminalWritten = true
		return msg, true
	}
	if !planned {
		return msg, true
	}
	return msg, false
}

func (s *sessionDurationTerminalState) writeObservedProviderTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle) error {
	if s == nil || s.terminalWritten || s.admittedInferencer == nil {
		return nil
	}
	msg, ok := s.admittedInferencer.providerTerminalMessage()
	if !ok {
		return nil
	}
	if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
		return err
	}
	s.terminalWritten = true
	return nil
}

func writeMaxDurationTerminal(out io.Writer, artifacts SessionDurationArtifactLifecycle, outputState messages.TerminalOutputState) error {
	return writeDurationSessionReplayMessage(out, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(SessionMaxDurationReason),
			string(SessionMaxDurationReason),
			SessionMaxDurationReason,
			messages.TerminalProvenanceLoop,
			outputState,
		),
	}, artifacts)
}

func sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr error) error {
	var lifecycleErrs []error
	if runtimeErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("session runtime", runtimeErr))
	}
	if closeErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("close session", closeErr))
	}
	if bindingErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("close RTC device binding", bindingErr))
	}
	return errors.Join(lifecycleErrs...)
}

func sessionTransportError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("session transport: %w", err)
}
