package service

import (
	"fmt"
	"net/http"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type capturedProvider struct {
	llmproviders.Provider
	recorder recording.Writer
}

func (p *capturedProvider) FlushToFile(path string) error { return p.recorder.FlushToFile(path) }

func (s *Service) httpRuntime(cfg providers.Config) (*Service, recording.Writer, error) {
	client := &http.Client{}
	if s.httpClient != nil {
		*client = *s.httpClient
	}
	if client.Transport == nil {
		client.Transport = http.DefaultTransport
	}
	var recorder recording.Writer
	if cfg.ReplayPath != "" {
		replay, err := gatewaytesting.NewReplayRoundTripper(cfg.ReplayPath)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load replay captures: %w", err)
		}
		client.Transport = replay
	} else if cfg.RecordPath != "" {
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		capture := gatewaytesting.NewRecordRoundTripper(transport)
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
