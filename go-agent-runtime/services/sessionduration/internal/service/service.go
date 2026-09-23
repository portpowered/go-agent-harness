package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationrunner "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/internal/runner"
)

// Service is the private implementation of the sessionduration contract.
type Service struct{}

const maxDurationReason = sessionduration.MaxDurationReason

func New() *Service { return &Service{} }

// Run executes one bounded session through the service-owned invocation loop.
func (s *Service) Run(request sessionduration.RunRequest) error {
	_, err := s.RunWithResult(request)
	return err
}

// Execute owns duration-run validation and the lifecycle order surrounding a
// host's startup and invocation effects. Finalization runs even when either
// effect returns an error.
func (s *Service) Execute(request sessionduration.ExecutionRequest) (runErr error) {
	ctx := request.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.ValidateDuration(request.MaxDuration); err != nil {
		return err
	}
	clock := request.Clock
	if clock == nil {
		clock = request.SourceClock
	}
	if clock == nil {
		clock = request.FallbackClock
	}
	if request.Finalization.Artifacts == nil {
		request.Finalization.Artifacts = s.ArtifactsFromContext(ctx)
	}
	finalizer := s.NewFinalizer(request.Finalization)
	defer func() {
		runErr = finalizer.Finish(ctx, request.Output, runErr)
	}()
	if request.Prepare != nil {
		if err := request.Prepare(ctx, request.Output); err != nil {
			return err
		}
	}
	if request.Run == nil {
		return nil
	}
	return request.Run(ctx, request.Output, clock)
}

// RunWithResult returns the service-owned terminal snapshot after cleanup.
func (s *Service) RunWithResult(request sessionduration.RunRequest) (sessionduration.Result, error) {
	if request.Context == nil {
		request.Context = context.Background()
	}
	if err := s.validateRunRequest(request); err != nil {
		return sessionduration.Result{}, err
	}
	admitted, err := s.runAdmission(request)
	if err != nil {
		return sessionduration.Result{}, err
	}
	deferAdmissionSessionClose(admitted)
	request.Close = closeAdmissionSessionAfterHost(admitted, request.Close)
	return durationrunner.RunWithResult(s, request, admitted, publish)
}

func (s *Service) validateRunRequest(request sessionduration.RunRequest) error {
	if err := s.ValidateDuration(request.MaxDuration); err != nil {
		return err
	}
	if request.Inferencer == nil && request.Admission == nil {
		return errors.New("session duration inferencer is required")
	}
	if request.LoopFactory == nil {
		return errors.New("session duration loop factory is required")
	}
	return nil
}

func (s *Service) runAdmission(request sessionduration.RunRequest) (sessionduration.AdmissionInferencer, error) {
	if request.Admission != nil {
		return request.Admission, nil
	}
	return s.NewAdmissionInferencer(request.Inferencer, s.NewEventAdmission(), nil), nil
}

type deferredAdmissionCloser interface {
	deferSessionCloseUntilFinalization()
	finalizeSessionClose() error
}

func deferAdmissionSessionClose(admitted sessionduration.AdmissionInferencer) {
	if closer, ok := admitted.(deferredAdmissionCloser); ok {
		closer.deferSessionCloseUntilFinalization()
	}
}

func closeAdmissionSessionAfterHost(admitted sessionduration.AdmissionInferencer, closeHost func() error) func() error {
	return func() error {
		var hostErr error
		if closeHost != nil {
			hostErr = closeHost()
		}
		if closer, ok := admitted.(deferredAdmissionCloser); ok {
			return errors.Join(hostErr, closer.finalizeSessionClose())
		}
		return hostErr
	}
}

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

func (f *finalizer) complete(out io.Writer, runErr error) error {
	var artifactErr error
	if f.ports.Artifacts != nil {
		artifactErr = invokeCleanup(func() error { return FinalizeArtifacts(f.ports.Artifacts) })
	}
	if f.ports.RecordArtifactFinalization != nil {
		recordErr := invokeCleanup(func() error {
			f.ports.RecordArtifactFinalization(f.ports.Artifacts != nil, artifactErr)
			return nil
		})
		artifactErr = errors.Join(artifactErr, recordErr)
	}
	runErr = errors.Join(runErr, artifactErr)

	var completionErr error
	canCompleteReplay := f.ports.HasIndependentFailure == nil || !f.ports.HasIndependentFailure(runErr)
	if canCompleteReplay && f.ports.CompleteReplay != nil {
		completionErr = invokeCleanup(func() error {
			f.ports.CompleteReplay()
			return nil
		})
	}
	if f.ports.PublishTerminal != nil {
		publishErr := invokeCleanup(func() error { return f.ports.PublishTerminal(out, runErr) })
		completionErr = errors.Join(completionErr, publishErr)
	}
	return errors.Join(artifactErr, completionErr)
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
