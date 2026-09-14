package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// Service is the private implementation of the sessionduration contract.
type Service struct{}

const maxDurationReason messages.TerminalReason = "max_duration"

func New() *Service { return &Service{} }

func (s *Service) NewState(source sessionduration.TerminalSource) sessionduration.State {
	return &terminalState{source: source}
}

func (s *Service) PublishMaxDuration(publication sessionduration.Publication, output messages.TerminalOutputState) error {
	return publishMaxDuration(publication, output)
}

func publishMaxDuration(publication sessionduration.Publication, output messages.TerminalOutputState) error {
	return publish(publication, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(maxDurationReason),
			string(maxDurationReason),
			maxDurationReason,
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

func (s *Service) ValidateDuration(duration time.Duration) error {
	if duration < 0 {
		return &sessionduration.InvalidDurationError{Duration: duration}
	}
	return nil
}

func (s *Service) NewEventAdmission() sessionduration.EventAdmission {
	return NewEventAdmission()
}

func (s *Service) NewAdmissionInferencer(inner messages.SessionInferencer, admission sessionduration.EventAdmission, closeDone chan struct{}) sessionduration.AdmissionInferencer {
	boundary, _ := admission.(*EventAdmission)
	return NewAdmissionInferencer(inner, boundary, closeDone)
}

func (s *Service) NewAdmissionSession(ctx context.Context, inner messages.Session, admission sessionduration.EventAdmission, onClose func(error)) sessionduration.AdmissionSession {
	boundary, _ := admission.(*EventAdmission)
	return NewAdmissionSession(ctx, inner, boundary, onClose)
}

func (s *Service) WithArtifacts(ctx context.Context, artifacts sessionduration.ArtifactLifecycle) context.Context {
	return WithSessionDurationArtifacts(ctx, artifacts)
}

func (s *Service) ArtifactsFromContext(ctx context.Context) sessionduration.ArtifactLifecycle {
	return ArtifactsFromContext(ctx)
}

func (s *Service) WithTerminalRecorder(ctx context.Context, recorder sessionduration.TerminalRecorder) context.Context {
	return WithTerminalRecorder(ctx, recorder)
}

func (s *Service) WithArtifactPaths(ctx context.Context, paths sessionduration.SessionDurationArtifactPaths) context.Context {
	return WithSessionDurationArtifactPaths(ctx, paths)
}

func (s *Service) PrepareArtifacts(ctx context.Context) (context.Context, error) {
	return PrepareArtifacts(ctx)
}

func (s *Service) FinalizeArtifacts(artifacts sessionduration.ArtifactLifecycle) error {
	return FinalizeArtifacts(artifacts)
}

func (s *Service) EvaluateRetry(policy sessionduration.RetryPolicy, terminal *messages.MessageEndValue) sessionduration.RetryDecision {
	return EvaluateRetry(policy, terminal)
}

func (s *Service) IsDurationShutdownMessage(msg messages.StreamMessage) bool {
	return IsDurationShutdownMessage(msg)
}

func (s *Service) IsDurationForwardMessage(msg messages.StreamMessage) bool {
	return IsDurationForwardMessage(msg)
}

func (s *Service) RecordingTerminalSummaryFromMessage(msg messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	return RecordingTerminalSummaryFromMessage(msg)
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
	case messages.StreamTypeTextStart,
		messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart,
		messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart,
		messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart,
		messages.StreamTypeFileEnd,
		messages.StreamTypeEmbeddingStart,
		messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeVADSpeechStarted,
		messages.StreamTypeVADSpeechStopped,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeInputItemAdded,
		messages.StreamTypePong,
		messages.StreamTypeSessionOpen,
		messages.StreamTypeSessionClose,
		messages.StreamTypeSessionCreated,
		messages.StreamTypeSessionUpdated,
		messages.StreamTypeSessionUpdate,
		messages.StreamTypeResponseCancel,
		messages.StreamTypeResponseCreate,
		messages.StreamTypeLoopEnd,
		messages.StreamTypeUsageInfo,
		messages.StreamTypeError,
		messages.StreamTypeSystemFullMessage:
		// These events do not change the accepted assistant-output projection.
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

func (s *terminalState) PublishMaxDuration(publication sessionduration.Publication, output messages.TerminalOutputState) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalWritten {
		return nil
	}
	if err := publishMaxDuration(publication, output); err != nil {
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
