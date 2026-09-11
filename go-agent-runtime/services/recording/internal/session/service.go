// Package session owns the runtime recording façade. It is intentionally
// independent of CLI configuration, flags, and browser implementations.
package session

import (
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service is the invocation factory. The existing semantic evidence service is
// injected explicitly so this package adds lifecycle composition without
// creating a second transcript/audio writer.
type Service struct {
	capture recording.Service
	clock   clock.Source
}

func New(capture recording.Service, source clock.Source) *Service {
	return &Service{capture: capture, clock: source}
}

func (s *Service) OpenSession(options recording.SessionOptions) (recording.SessionRecorder, error) {
	if s == nil || s.capture == nil {
		return nil, errors.New("recording session requires a capture service")
	}
	if s.clock == nil {
		return nil, errors.New("recording session requires a clock")
	}
	if options.Destination == "" {
		return nil, errors.New("recording session requires a destination")
	}
	base := s.clock.Now()
	if options.ClockBase.IsZero() {
		options.ClockBase = base
	}
	if options.WallClockStart.IsZero() {
		options.WallClockStart = base
	}
	options.Credentials = append([]string(nil), options.Credentials...)
	options.AdditionalArtifacts = cloneArtifacts(options.AdditionalArtifacts)
	live, err := s.capture.OpenLiveEvidence(recording.LiveEvidenceOptions{
		Destination:                   options.Destination,
		SessionID:                     options.SessionID,
		ParticipantID:                 options.ParticipantID,
		Provider:                      options.Provider,
		Model:                         options.Model,
		ClockBase:                     options.ClockBase,
		WallClockStart:                options.WallClockStart,
		Credentials:                   options.Credentials,
		ProviderCapturePath:           options.ProviderCapturePath,
		DisableProviderCaptureSidecar: options.DisableProviderCaptureSidecar,
		Limits:                        options.Limits,
	})
	if err != nil {
		return nil, err
	}
	r := &recorder{
		live:    live,
		clock:   s.clock,
		options: options,
		images:  make(map[string]imageEvidence),
	}
	if options.Browser.Enabled {
		r.browser = newBrowserRecorder(options.Browser, options.Credentials, options.ClockBase)
		r.browser.start()
	}
	return r, nil
}

func cloneArtifacts(input []transcript.RecordingArtifact) []transcript.RecordingArtifact {
	if len(input) == 0 {
		return nil
	}
	output := make([]transcript.RecordingArtifact, len(input))
	for i, artifact := range input {
		output[i] = artifact
		output[i].Data = append([]byte(nil), artifact.Data...)
	}
	return output
}

type recorder struct {
	live    runtimesession.LiveRecorder
	clock   clock.Source
	options recording.SessionOptions
	browser *browserRecorder

	mu           sync.Mutex
	observeMu    sync.Mutex
	closed       bool
	images       map[string]imageEvidence
	imageOrder   []string
	inputAudio   [][]byte
	outputAudio  [][]byte
	audio        audioProjection
	toolOrder    []toolObservation
	terminal     *transcript.RecordingTerminalSummary
	firstErr     error
	finalizeOnce sync.Once
	finalizeErr  error
}

var _ recording.SessionService = (*Service)(nil)
var _ recording.SessionRecorder = (*recorder)(nil)
