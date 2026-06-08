package capabilities

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCapabilityStateSemantics(t *testing.T) {
	t.Parallel()

	if !CapabilityStateSupported.IsSupported() {
		t.Fatalf("supported state should report support")
	}
	if CapabilityStateUnknown.IsSupported() {
		t.Fatalf("unknown state must not report support")
	}
	if !CapabilityStateUnsupported.IsKnown() {
		t.Fatalf("unsupported state should be known")
	}
	if CapabilityStateUnknown.IsKnown() {
		t.Fatalf("unknown state should not be known")
	}
}

func TestUnknownProviderCapabilitiesDoesNotClaimSupport(t *testing.T) {
	t.Parallel()

	caps := UnknownProviderCapabilities("legacy")
	if caps.Provider != "legacy" {
		t.Fatalf("provider = %q, want legacy", caps.Provider)
	}

	features := []FeatureCapability{
		caps.Stateless.Tools,
		caps.Stateless.Streaming,
		caps.Stateless.ImageInput,
		caps.Stateless.AudioInput,
		caps.Stateless.AudioOutput,
		caps.Stateless.VideoOutput,
		caps.Stateless.Reasoning,
		caps.Stateless.PromptCaching,
		caps.Stateless.ProviderSpecificConfig,
		caps.Session.Sessions,
		caps.Session.Tools,
		caps.Session.AudioInput,
		caps.Session.AudioOutput,
		caps.Session.ProviderSpecificConfig,
	}

	for i, feature := range features {
		if feature.State != CapabilityStateUnknown {
			t.Fatalf("feature %d state = %q, want unknown", i, feature.State)
		}
		if feature.IsSupported() {
			t.Fatalf("feature %d unexpectedly reports support", i)
		}
	}
}

func TestProviderCapabilitiesJSONRoundTrip(t *testing.T) {
	t.Parallel()

	in := ProviderCapabilities{
		Provider: "test-provider",
		Stateless: StatelessCapabilities{
			Tools:                  Supported("tool calls supported"),
			Streaming:              Unsupported("stream API unavailable"),
			ImageInput:             Unknown("not evaluated"),
			AudioInput:             Unsupported("stateless audio input unavailable"),
			AudioOutput:            Unsupported("stateless audio output unavailable"),
			VideoOutput:            Unsupported("video generation unavailable"),
			Reasoning:              Supported("thinking budget accepted"),
			PromptCaching:          Unknown("model-specific"),
			ProviderSpecificConfig: Supported("raw config passthrough"),
		},
		Session: SessionCapabilities{
			Sessions:               Unsupported("no session transport"),
			Tools:                  Unknown("session tools not evaluated"),
			AudioInput:             Unknown("session audio input not evaluated"),
			AudioOutput:            Unknown("session audio output not evaluated"),
			ProviderSpecificConfig: Unknown("session config not evaluated"),
		},
		Metadata: map[string]string{"source": "unit-test"},
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ProviderCapabilities
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", got, in)
	}
}
