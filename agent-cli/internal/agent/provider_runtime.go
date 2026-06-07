package agent

import (
	"fmt"
	"net/http"

	"github.com/portpowered/go-llm-gateway/pkg/testing"
)

// ProviderHTTPRuntime centralizes the shared HTTP runtime ownership for provider construction.
type ProviderHTTPRuntime struct {
	Client   *http.Client
	Recorder *testing.RecordRoundTripper
}

func buildProviderHTTPRuntime(cfg *Config) (ProviderHTTPRuntime, error) {
	if cfg == nil {
		cfg = &Config{}
	}

	if cfg.ReplayCapturePath != "" {
		replayRT, err := testing.NewReplayRoundTripper(cfg.ReplayCapturePath)
		if err != nil {
			return ProviderHTTPRuntime{}, fmt.Errorf("failed to load replay captures: %w", err)
		}
		return ProviderHTTPRuntime{
			Client: &http.Client{Transport: replayRT},
		}, nil
	}

	if cfg.RecordCapturePath != "" {
		recorder := testing.NewRecordRoundTripper(http.DefaultTransport)
		return ProviderHTTPRuntime{
			Client:   &http.Client{Transport: recorder},
			Recorder: recorder,
		}, nil
	}

	return ProviderHTTPRuntime{
		Client: &http.Client{Transport: http.DefaultTransport},
	}, nil
}
