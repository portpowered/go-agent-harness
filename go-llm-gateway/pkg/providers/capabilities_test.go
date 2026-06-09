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

func TestConcreteProviderFamiliesReportEveryPublicCapability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reporter providers.CapabilityReporter
	}{
		{name: "anthropic", reporter: anthropic.New()},
		{name: "openai", reporter: openai.New()},
		{name: "gemini", reporter: gemini.New()},
		{name: "grok", reporter: grok.New()},
		{name: "fal", reporter: fal.New()},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.reporter.Capabilities()
			if got.Provider == "" {
				t.Fatal("Provider is empty")
			}

			features := map[string]capabilities.FeatureCapability{
				"stateless.tools":                  got.Stateless.Tools,
				"stateless.streaming":              got.Stateless.Streaming,
				"stateless.imageInput":             got.Stateless.ImageInput,
				"stateless.audioInput":             got.Stateless.AudioInput,
				"stateless.audioOutput":            got.Stateless.AudioOutput,
				"stateless.videoOutput":            got.Stateless.VideoOutput,
				"stateless.reasoning":              got.Stateless.Reasoning,
				"stateless.promptCaching":          got.Stateless.PromptCaching,
				"stateless.providerSpecificConfig": got.Stateless.ProviderSpecificConfig,
				"session.sessions":                 got.Session.Sessions,
				"session.tools":                    got.Session.Tools,
				"session.audioInput":               got.Session.AudioInput,
				"session.audioOutput":              got.Session.AudioOutput,
				"session.providerSpecificConfig":   got.Session.ProviderSpecificConfig,
			}

			for name, feature := range features {
				switch feature.State {
				case capabilities.CapabilityStateSupported,
					capabilities.CapabilityStateUnsupported,
					capabilities.CapabilityStateUnknown:
				default:
					t.Errorf("%s state = %q, want supported, unsupported, or unknown", name, feature.State)
				}
				if feature.Detail == "" {
					t.Errorf("%s detail is empty", name)
				}
			}
		})
	}
}
