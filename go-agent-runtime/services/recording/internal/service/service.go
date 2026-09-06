package service

import (
	"errors"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// Service is an inert capture factory; invocation resources live in handles.
type Service struct{ clock clock.Source }

func New(source clock.Source) *Service { return &Service{clock: source} }

func (*Service) TrackSession(inner messages.SessionInferencer, writer recording.Writer, path string) (recording.SessionCapture, error) {
	if inner == nil || writer == nil || path == "" {
		return nil, errors.New("recording requires a session, writer and destination")
	}
	return &recordingSessionInferencer{inner: inner, recorder: writer, path: path, flushDone: make(chan struct{})}, nil
}

func (s *Service) OpenLiveEvidence(options recording.LiveEvidenceOptions) (session.LiveRecorder, error) {
	return evidence.New(options, s.clock)
}
