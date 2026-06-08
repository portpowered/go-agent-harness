package providers_test

import (
	"testing"

	"github.com/portpowered/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-llm-gateway/pkg/providers/anthropic"
	"github.com/portpowered/go-llm-gateway/pkg/providers/fal"
	"github.com/portpowered/go-llm-gateway/pkg/providers/gemini"
	"github.com/portpowered/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-llm-gateway/pkg/providers/openai"
)

func TestConcreteProviderFamiliesReportCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		reporter     providers.CapabilityReporter
		wantProvider string
		supported    func(capabilities.ProviderCapabilities) capabilities.FeatureCapability
		unsupported  func(capabilities.ProviderCapabilities) capabilities.FeatureCapability
	}{
		{
			name:         "anthropic",
			reporter:     anthropic.New(),
			wantProvider: "anthropic",
			supported:    func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Stateless.Tools },
			unsupported: func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability {
				return c.Stateless.AudioInput
			},
		},
		{
			name:         "openai",
			reporter:     openai.New(),
			wantProvider: "openai",
			supported:    func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Session.Sessions },
			unsupported:  func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Stateless.Reasoning },
		},
		{
			name:         "gemini",
			reporter:     gemini.New(),
			wantProvider: "gemini",
			supported:    func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Stateless.Streaming },
			unsupported: func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability {
				return c.Stateless.PromptCaching
			},
		},
		{
			name:         "grok",
			reporter:     grok.New(),
			wantProvider: "grok",
			supported:    func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Session.Sessions },
			unsupported:  func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Stateless.Streaming },
		},
		{
			name:         "fal",
			reporter:     fal.New(),
			wantProvider: "fal",
			supported: func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability {
				return c.Stateless.VideoOutput
			},
			unsupported: func(c capabilities.ProviderCapabilities) capabilities.FeatureCapability { return c.Stateless.Streaming },
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.reporter.Capabilities()
			if got.Provider != tt.wantProvider {
				t.Fatalf("Provider = %q, want %q", got.Provider, tt.wantProvider)
			}
			if state := tt.supported(got).State; state != capabilities.CapabilityStateSupported {
				t.Fatalf("supported capability state = %q, want supported", state)
			}
			unsupported := tt.unsupported(got)
			if unsupported.State != capabilities.CapabilityStateUnsupported {
				t.Fatalf("unsupported capability state = %q, want unsupported", unsupported.State)
			}
			if unsupported.Detail == "" {
				t.Fatal("unsupported capability detail is empty")
			}
		})
	}
}
