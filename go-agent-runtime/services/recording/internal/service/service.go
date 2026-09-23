package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// Service is an inert capture factory; invocation resources live in handles.
type Service struct{ clock clock.Source }

func New(source clock.Source) *Service { return &Service{clock: source} }

func (s *Service) Claim(options recording.ClaimOptions) (recording.DestinationClaim, error) {
	return evidence.Claim(options)
}

func (*Service) TrackSession(inner messages.SessionInferencer, writer recording.Writer, path string) (recording.SessionCapture, error) {
	if inner == nil || writer == nil || path == "" {
		return nil, errors.New("recording requires a session, writer and destination")
	}
	return &recordingSessionInferencer{inner: inner, recorder: writer, path: path, flushDone: make(chan struct{})}, nil
}

func (s *Service) TrackInjectedSession(inner messages.SessionInferencer, path string) (recording.SessionCapture, error) {
	if inner == nil || path == "" {
		return nil, errors.New("injected recording requires a session and destination")
	}
	claim, err := s.Claim(recording.ClaimOptions{Destination: path, Kind: recording.ClaimKindCapture})
	if err != nil {
		return nil, err
	}
	return &injectedSessionCapture{
		inner:   gatewaytesting.NewRecordingSessionInferencerWithOptions(inner),
		path:    path,
		claim:   claim,
		flushed: make(chan struct{}),
	}, nil
}

func (s *Service) RecordProviderSession(captureService recording.ProviderCaptureService, options recording.ProviderSessionOptions) (recording.SessionCapture, error) {
	if captureService == nil || options.Destination == "" || options.Provider == "" || options.Model == "" || options.Dialer == nil || options.Build == nil {
		return nil, errors.New("provider recording requires a capture service, destination, provider, model, dialer and builder")
	}
	sink, err := captureService.OpenProviderCapture(recording.ProviderCaptureOptions{Destination: options.Destination})
	if err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, errors.New("provider capture service returned no sink")
	}
	writer, err := newProviderRecordingDialer(options.Dialer, options.Provider, options.Model, sink, options.Clock)
	if err != nil {
		return nil, errors.Join(err, sink.Abort())
	}
	inner, err := options.Build(writer)
	if err != nil {
		return nil, errors.Join(err, sink.Abort())
	}
	capture, err := s.TrackSession(inner, writer, options.Destination)
	if err != nil {
		return nil, errors.Join(err, sink.Abort())
	}
	return capture, nil
}

func (s *Service) OpenLiveEvidence(options recording.LiveEvidenceOptions) (session.LiveRecorder, error) {
	return evidence.New(options, s.clock)
}

func (s *Service) RunLiveEvidence(ctx context.Context, options recording.LiveEvidenceOptions, run func(context.Context, recording.LiveEvidence) error) (runErr error) {
	if run == nil {
		return errors.New("recording runtime callback is required")
	}
	options.Credentials = append([]string(nil), options.Credentials...)
	recorder, err := s.OpenLiveEvidence(options)
	if err != nil {
		return err
	}
	evidence := &liveEvidence{recorder: recorder, options: options, clock: s.clock}
	defer func() {
		panicValue := recover()
		terminalErr := runErr
		if panicValue != nil {
			terminalErr = errors.Join(terminalErr, fmt.Errorf("recording runtime callback panicked: %v", panicValue))
		}
		if !evidence.hasCompletion() {
			completion := recording.LiveCompletion{RunError: terminalErr}
			if err := evidence.SetCompletion(context.Background(), completion); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
		failure := evidence.failure()
		terminalErr = errors.Join(terminalErr, failure)
		runErr = errors.Join(runErr, failure)
		finalizeCtx := context.Background()
		if ctx != nil {
			finalizeCtx = context.WithoutCancel(ctx)
		}
		runErr = errors.Join(runErr, recorder.Finalize(finalizeCtx, terminalErr))
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	return run(ctx, evidence)
}

type liveEvidence struct {
	recorder      session.LiveRecorder
	options       recording.LiveEvidenceOptions
	clock         clock.Source
	mu            sync.Mutex
	completionSet bool
	terminalSet   bool
	firstErr      error
}

func (e *liveEvidence) latch(err error) error {
	if err == nil {
		return nil
	}
	e.mu.Lock()
	if e.firstErr == nil {
		e.firstErr = err
	}
	e.mu.Unlock()
	return err
}

func (e *liveEvidence) failure() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.firstErr
}

func (e *liveEvidence) hasCompletion() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.completionSet
}

func (e *liveEvidence) ObserveMessage(ctx context.Context, direction session.LiveRecordDirection, message messages.StreamMessage) error {
	if err := e.recorder.RecordMessage(ctx, session.LiveRecord{Direction: direction, Timestamp: e.clock.Now(), Message: message}); err != nil {
		return e.latch(err)
	}
	if direction == session.LiveRecordAgent {
		if err := e.observeAudioMessage(ctx, message); err != nil {
			return e.latch(err)
		}
	}
	switch message.Type {
	case messages.StreamTypeSessionClose:
		terminal, ok := message.Value.(*messages.SessionCloseValue)
		if ok && terminal != nil {
			return e.latch(e.recordTerminal(ctx, terminal, nil))
		}
	case messages.StreamTypeError:
		value, ok := message.Value.(*messages.ErrorValue)
		if ok && value != nil && value.IsTerminal() {
			return e.latch(e.recordTerminal(ctx, terminalValueFromError(value), value.Err))
		}
	}
	return nil
}

func (e *liveEvidence) ObserveAudio(ctx context.Context, observation recording.LiveAudioObservation) error {
	return e.latch(e.recorder.RecordAudio(ctx, session.LiveAudioRecord{
		Direction: observation.Direction, Admission: observation.Admission,
		Timestamp: e.clock.Now(), Frame: observation.Frame,
	}))
}

func (e *liveEvidence) observeAudioMessage(ctx context.Context, message messages.StreamMessage) error {
	rate := e.options.OutputAudioRate
	if rate <= 0 || (message.Type != messages.StreamTypeAudioDelta && message.Type != messages.StreamTypeAudioEnd) {
		return nil
	}
	frame := audio.PCMFrame{Format: audio.PCM16DeviceFormat(rate), PlaybackResponse: audio.PlaybackResponse{ResponseID: message.ResponseID}}
	if message.Type == messages.StreamTypeAudioEnd {
		frame.EndOfResponse = true
	} else {
		value, ok := message.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil || len(value.Content) == 0 {
			return nil
		}
		samples, err := codec.DecodePCM16(value.Content)
		if err != nil {
			return err
		}
		frame.Samples = samples
	}
	return e.ObserveAudio(ctx, recording.LiveAudioObservation{
		Direction: session.LiveRecordAgent, Admission: session.LiveAudioMessageObserved, Frame: frame,
	})
}

func (e *liveEvidence) SetCompletion(ctx context.Context, completion recording.LiveCompletion) error {
	e.mu.Lock()
	if e.completionSet {
		e.mu.Unlock()
		return nil
	}
	e.completionSet = true
	terminalAlreadySet := e.terminalSet
	e.mu.Unlock()
	if terminalAlreadySet {
		return nil
	}

	reason := messages.TerminalReasonLoopSynthesizedCompletion
	classification := string(reason)
	provenance := messages.TerminalProvenanceLoop
	output := messages.TerminalOutputNone
	if completion.SawSessionOpen && (completion.TurnsCompleted > 0 || completion.OutputObserved) {
		output = messages.TerminalOutputPartial
	}
	switch {
	case completion.DurationExpired:
		reason, classification, output = messages.TerminalReason("max_duration"), "max_duration", messages.TerminalOutputPartial
	case completion.UserCancelled || completion.RoomCancellationOnly:
		reason, classification, provenance = messages.TerminalReasonCancellation, string(messages.TerminalReasonCancellation), messages.TerminalProvenanceCLI
	case completion.RunError != nil:
		reason, classification, provenance = messages.TerminalReasonTerminalFailure, string(messages.TerminalReasonTerminalFailure), messages.TerminalProvenanceSession
	}
	text := ""
	if completion.DurationExpired {
		text = "max_duration"
	} else if completion.RunError != nil {
		text = completion.RunError.Error()
	}
	terminal := messages.NewSessionCloseValueWithTerminal("", text, classification, reason, provenance, output)
	return e.latch(e.recordTerminal(ctx, terminal, completion.RunError))
}

func (e *liveEvidence) recordTerminal(ctx context.Context, terminal *messages.SessionCloseValue, terminalErr error) error {
	e.mu.Lock()
	if e.terminalSet {
		e.mu.Unlock()
		return nil
	}
	e.terminalSet = true
	e.mu.Unlock()
	return e.latch(e.recorder.RecordEvent(ctx, session.LiveEvent{
		Timestamp: e.clock.Now(), Kind: string(session.LiveEventTerminal), Terminal: terminal,
		Error: terminalErr, Critical: true,
	}))
}

func terminalValueFromError(value *messages.ErrorValue) *messages.SessionCloseValue {
	reason := value.TerminalReason
	if reason == "" {
		reason = messages.TerminalReasonTerminalFailure
	}
	classification := value.Classification
	if classification == "" {
		classification = string(reason)
	}
	provenance := value.TerminalProvenance
	if provenance == "" {
		provenance = messages.TerminalProvenanceProvider
	}
	output := value.OutputState
	if output == "" {
		output = messages.TerminalOutputNone
	}
	return messages.NewSessionCloseValueWithTerminal("", value.Message, classification, reason, provenance, messages.TerminalOutputState(output))
}

func (e *liveEvidence) RecordBrowserArtifact(ctx context.Context, artifact *transcript.BrowserArtifact) error {
	recorder, ok := e.recorder.(recording.BrowserArtifactRecorder)
	if !ok {
		return errors.New("recording runtime does not accept browser artifacts")
	}
	return e.latch(recorder.RecordBrowserArtifact(ctx, artifact))
}

func (e *liveEvidence) ProviderCapturePath() string {
	if e == nil {
		return ""
	}
	provider, ok := e.recorder.(recording.ProviderCapture)
	if !ok {
		return ""
	}
	return provider.ProviderCapturePath()
}

func (*Service) OpenProviderCapture(options recording.ProviderCaptureOptions) (recording.ProviderCaptureSink, error) {
	return evidence.NewProviderCaptureWithLimits(options.Destination, options.Limits)
}

func (*Service) OpenLiveSemanticEvidence(providerCapturePath string) (session.LiveRecorder, error) {
	return evidence.NewSemanticSidecar(providerCapturePath)
}

var _ recording.ProviderCaptureService = (*Service)(nil)
