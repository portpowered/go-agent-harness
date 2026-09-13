package service

import (
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// Service is the private implementation of the sessionduration contract.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) NewState(source sessionduration.TerminalSource) sessionduration.State {
	return &terminalState{source: source}
}

func (s *Service) PublishMaxDuration(publication sessionduration.Publication, output messages.TerminalOutputState) error {
	return publish(publication, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(sessionduration.MaxDurationReason),
			string(sessionduration.MaxDurationReason),
			sessionduration.MaxDurationReason,
			messages.TerminalProvenanceLoop,
			output,
		),
	})
}

func (s *Service) LifecycleError(failures sessionduration.LifecycleFailures) error {
	var joined []error
	if failures.Runtime != nil {
		joined = append(joined, phaseError("session runtime", failures.Runtime))
	}
	if failures.Close != nil {
		joined = append(joined, phaseError("close session", failures.Close))
	}
	if failures.Binding != nil {
		joined = append(joined, phaseError("close RTC device binding", failures.Binding))
	}
	return errors.Join(joined...)
}

func (s *Service) TransportError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("session transport: %w", err)
}

func phaseError(phase string, err error) error { return fmt.Errorf("%s: %w", phase, err) }

func publish(publication sessionduration.Publication, msg messages.StreamMessage) error {
	if publication.Artifacts != nil {
		if err := publication.Artifacts.Accept(msg); err != nil {
			return phaseError("write duration artifacts", err)
		}
	}
	if publication.Write != nil {
		return publication.Write(msg)
	}
	return nil
}

type terminalState struct {
	mu               sync.Mutex
	source           sessionduration.TerminalSource
	terminalWritten  bool
	responseOutput   bool
	responseComplete bool
}

func (s *terminalState) Observe(msg messages.StreamMessage) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observe(msg)
}

func (s *terminalState) observe(msg messages.StreamMessage) {
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

func (s *terminalState) OutputState() messages.TerminalOutputState {
	if s == nil {
		return messages.TerminalOutputNone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outputState()
}

func (s *terminalState) outputState() messages.TerminalOutputState {
	if !s.responseOutput {
		return messages.TerminalOutputNone
	}
	if s.responseComplete {
		return messages.TerminalOutputComplete
	}
	return messages.TerminalOutputPartial
}

func (s *terminalState) Admit(planned bool, msg messages.StreamMessage) (messages.StreamMessage, bool) {
	if s == nil || msg.Type != messages.StreamTypeSessionClose {
		return msg, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalWritten {
		return msg, false
	}
	if s.source.Matches != nil && s.source.Matches(msg) {
		s.terminalWritten = true
		return msg, true
	}
	if !planned {
		return msg, true
	}
	return msg, false
}

func (s *terminalState) PublishProviderTerminal(publication sessionduration.Publication) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalWritten || s.source.Message == nil {
		return nil
	}
	msg, ok := s.source.Message()
	if !ok {
		return nil
	}
	if err := publish(publication, msg); err != nil {
		return err
	}
	s.terminalWritten = true
	return nil
}

func (s *terminalState) Written() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalWritten
}
