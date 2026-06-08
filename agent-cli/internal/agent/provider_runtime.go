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

type providerHTTPRuntimeOptions struct {
	baseTransport http.RoundTripper
}

// ProviderHTTPRuntimeOption configures the CLI-owned HTTP runtime before it is
// injected into provider builders.
type ProviderHTTPRuntimeOption func(*providerHTTPRuntimeOptions)

// WithProviderHTTPBaseTransport sets the live transport used directly in live
// mode and wrapped by the recorder in record mode.
func WithProviderHTTPBaseTransport(transport http.RoundTripper) ProviderHTTPRuntimeOption {
	return func(opts *providerHTTPRuntimeOptions) {
		opts.baseTransport = transport
	}
}

func buildProviderHTTPRuntime(cfg *Config, opts ...ProviderHTTPRuntimeOption) (ProviderHTTPRuntime, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	runtimeOpts := providerHTTPRuntimeOptions{
		baseTransport: http.DefaultTransport,
	}
	for _, opt := range opts {
		opt(&runtimeOpts)
	}
	if runtimeOpts.baseTransport == nil {
		runtimeOpts.baseTransport = http.DefaultTransport
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
		recorder := testing.NewRecordRoundTripper(runtimeOpts.baseTransport)
		return ProviderHTTPRuntime{
			Client:   &http.Client{Transport: recorder},
			Recorder: recorder,
		}, nil
	}

	return ProviderHTTPRuntime{
		Client: &http.Client{Transport: runtimeOpts.baseTransport},
	}, nil
}
