package openai

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestOpenAIProviderCapabilitiesReportsLocalWrapperEvidence(t *testing.T) {
	t.Parallel()

	var reporter providers.CapabilityReporter = New()
	got := reporter.Capabilities()

	if got.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", got.Provider)
	}
	if got.Stateless.Tools.State != capabilities.CapabilityStateSupported {
		t.Fatalf("Stateless.Tools = %q, want supported", got.Stateless.Tools.State)
	}
	if got.Stateless.Streaming.State != capabilities.CapabilityStateSupported {
		t.Fatalf("Stateless.Streaming = %q, want supported", got.Stateless.Streaming.State)
	}
	if got.Stateless.ImageInput.State != capabilities.CapabilityStateSupported {
		t.Fatalf("Stateless.ImageInput = %q, want supported", got.Stateless.ImageInput.State)
	}
	if got.Stateless.AudioInput.State != capabilities.CapabilityStateSupported {
		t.Fatalf("Stateless.AudioInput = %q, want supported", got.Stateless.AudioInput.State)
	}
	if got.Session.Sessions.State != capabilities.CapabilityStateSupported {
		t.Fatalf("Session.Sessions = %q, want supported", got.Session.Sessions.State)
	}
	if got.Session.AudioOutput.State != capabilities.CapabilityStateSupported {
		t.Fatalf("Session.AudioOutput = %q, want supported", got.Session.AudioOutput.State)
	}
}

func TestOpenAIProviderCapabilitiesKeepUnsupportedGapsExplicit(t *testing.T) {
	t.Parallel()

	got := New().Capabilities()

	tests := []struct {
		name string
		got  capabilities.FeatureCapability
	}{
		{name: "stateless video output", got: got.Stateless.VideoOutput},
		{name: "stateless reasoning request config", got: got.Stateless.Reasoning},
		{name: "stateless prompt caching request config", got: got.Stateless.PromptCaching},
		{name: "stateless provider-specific config", got: got.Stateless.ProviderSpecificConfig},
		{name: "session provider-specific config", got: got.Session.ProviderSpecificConfig},
	}
	for _, tt := range tests {
		if tt.got.State != capabilities.CapabilityStateUnsupported {
			t.Errorf("%s state = %q, want unsupported", tt.name, tt.got.State)
		}
		if tt.got.Detail == "" {
			t.Errorf("%s detail is empty", tt.name)
		}
	}
}
