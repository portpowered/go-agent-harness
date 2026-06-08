package providers_test

import (
	"testing"

	"github.com/portpowered/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-llm-gateway/pkg/providers/anthropic"
	"github.com/portpowered/go-llm-gateway/pkg/providers/fal"
	"github.com/portpowered/go-llm-gateway/pkg/providers/gemini"
	"github.com/portpowered/go-llm-gateway/pkg/providers/grok"
	"github.com/portpowered/go-llm-gateway/pkg/providers/openai"
)

func TestProviderCapabilities_ReportFamilyStates(t *testing.T) {
	tests := []struct {
		name        string
		reporter    providers.CapabilityReporter
		want        string
		supported   func(providers.ProviderCapabilities) providers.Capability
		unsupported func(providers.ProviderCapabilities) providers.Capability
	}{
		{
			name:        "anthropic",
			reporter:    anthropic.New(),
			want:        "anthropic",
			supported:   func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.Reasoning },
			unsupported: func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.AudioInput },
		},
		{
			name:        "gemini",
			reporter:    gemini.New(),
			want:        "gemini",
			supported:   func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.AudioInput },
			unsupported: func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.VideoInput },
		},
		{
			name:        "fal",
			reporter:    fal.New(),
			want:        "fal",
			supported:   func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.VideoOutput },
			unsupported: func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.Streaming },
		},
		{
			name:        "grok",
			reporter:    grok.New(),
			want:        "grok",
			supported:   func(c providers.ProviderCapabilities) providers.Capability { return c.Session.Sessions },
			unsupported: func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.Inference },
		},
		{
			name:        "openai",
			reporter:    openai.New(),
			want:        "openai",
			supported:   func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.Streaming },
			unsupported: func(c providers.ProviderCapabilities) providers.Capability { return c.Stateless.VideoInput },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capabilities := tt.reporter.Capabilities()
			if capabilities.Provider != tt.want {
				t.Fatalf("Provider = %q, want %q", capabilities.Provider, tt.want)
			}

			if got := tt.supported(capabilities); got.State != providers.CapabilitySupported {
				t.Fatalf("supported capability state = %q, want %q", got.State, providers.CapabilitySupported)
			}

			gotUnsupported := tt.unsupported(capabilities)
			if gotUnsupported.State != providers.CapabilityUnsupported && gotUnsupported.State != providers.CapabilityUnknown {
				t.Fatalf("unsupported/unknown capability state = %q", gotUnsupported.State)
			}
			if gotUnsupported.Rationale == "" {
				t.Fatal("unsupported/unknown capability should include rationale")
			}
		})
	}
}

func TestProviderCapabilities_ReportSessionAudioFormats(t *testing.T) {
	tests := []struct {
		name     string
		reporter providers.CapabilityReporter
	}{
		{name: "openai", reporter: openai.New()},
		{name: "grok", reporter: grok.New()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capabilities := tt.reporter.Capabilities()
			if capabilities.Session == nil {
				t.Fatal("Session capabilities are nil")
			}
			for format, capability := range capabilities.Session.InputAudioFormats {
				if capability.State != providers.CapabilitySupported {
					t.Fatalf("input audio format %q state = %q, want supported", format, capability.State)
				}
			}
			for format, capability := range capabilities.Session.OutputAudioFormats {
				if capability.State != providers.CapabilitySupported {
					t.Fatalf("output audio format %q state = %q, want supported", format, capability.State)
				}
			}
		})
	}
}
