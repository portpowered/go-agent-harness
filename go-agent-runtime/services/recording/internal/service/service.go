package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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

func (s *Service) OpenLiveEvidence(options recording.LiveEvidenceOptions) (session.LiveRecorder, error) {
	return evidence.New(options, s.clock)
}

func (s *Service) RunLiveEvidence(ctx context.Context, options recording.LiveEvidenceOptions, run func(context.Context, session.LiveRecorder) error) (runErr error) {
	if run == nil {
		return errors.New("recording runtime callback is required")
	}
	recorder, err := s.OpenLiveEvidence(options)
	if err != nil {
		return err
	}
	defer func() {
		panicValue := recover()
		terminalErr := runErr
		if panicValue != nil {
			terminalErr = errors.Join(terminalErr, fmt.Errorf("recording runtime callback panicked: %v", panicValue))
		}
		finalizeCtx := context.Background()
		if ctx != nil {
			finalizeCtx = context.WithoutCancel(ctx)
		}
		runErr = errors.Join(runErr, recorder.Finalize(finalizeCtx, terminalErr))
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	return run(ctx, recorder)
}

func (*Service) OpenProviderCapture(options recording.ProviderCaptureOptions) (recording.ProviderCaptureSink, error) {
	return evidence.NewProviderCaptureWithLimits(options.Destination, options.Limits)
}

func (*Service) OpenLiveSemanticEvidence(providerCapturePath string) (session.LiveRecorder, error) {
	return evidence.NewSemanticSidecar(providerCapturePath)
}

var _ recording.ProviderCaptureService = (*Service)(nil)
