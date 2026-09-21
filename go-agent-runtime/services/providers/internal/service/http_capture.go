package service

import (
	"fmt"
	"net/http"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

type capturedProvider struct {
	llmproviders.Provider
	recorder recording.HTTPRecorder
}

func (p *capturedProvider) FlushToFile(path string) error { return p.recorder.FlushToFile(path) }

func (s *Service) httpRuntime(cfg providers.Config) (*Service, recording.HTTPRecorder, error) {
	client := &http.Client{}
	if s.httpClient != nil {
		*client = *s.httpClient
	}
	if client.Transport == nil {
		client.Transport = http.DefaultTransport
	}
	var recorder recording.HTTPRecorder
	if cfg.ReplayPath != "" {
		replayService, ok := s.replay.(runtimeReplay.HTTPReplayService)
		if !ok {
			return nil, nil, fmt.Errorf("replay service does not provide HTTP replay")
		}
		replay, err := replayService.OpenHTTPReplay(cfg.ReplayPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load replay captures: %w", err)
		}
		client.Transport = replay
	} else if cfg.RecordPath != "" {
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		recordingService, ok := s.recording.(recording.HTTPRecordingService)
		if !ok {
			return nil, nil, fmt.Errorf("recording service does not provide HTTP recording")
		}
		capture, err := recordingService.OpenHTTPRecorder(transport)
		if err != nil {
			return nil, nil, fmt.Errorf("open HTTP recording: %w", err)
		}
		recorder = capture
		client.Transport = capture
	}
	invocation := *s
	invocation.httpClient = client
	return &invocation, recorder, nil
}

func (p *capturedProvider) Capabilities() llmproviders.ProviderCapabilities {
	if reporter, ok := p.Provider.(llmproviders.CapabilityReporter); ok {
		return reporter.Capabilities()
	}
	return llmproviders.UnknownProviderCapabilities(p.Provider.Name())
}
