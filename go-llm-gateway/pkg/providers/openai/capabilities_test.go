package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// Fixture values shared by the provider tests.
const (
	testGreeting       = "Hello"
	testToolGetWeather = "get_weather"
)

func TestOpenAIProviderCapabilitiesReportsLocalWrapperEvidence(t *testing.T) {
	t.Parallel()

	var reporter providers.CapabilityReporter = New()
	got := reporter.Capabilities()

	if got.Provider != openAIProviderName {
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

// closeForTest closes a test-owned resource and reports an unexpected close
// failure without stopping the test.
func closeForTest(tb testing.TB, resource io.Closer) {
	tb.Helper()
	if err := resource.Close(); err != nil {
		tb.Errorf("close %T: %v", resource, err)
	}
}

// mustMarshalFixture encodes a fixture value built by the test itself; an
// encoding failure is a broken fixture, not a behavior under test.
func mustMarshalFixture(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal test fixture: %v", err))
	}
	return data
}

func realtimeSessionForTest(tb testing.TB, session messages.Session) *realtimeSession {
	tb.Helper()
	realtime, ok := session.(*realtimeSession)
	if !ok {
		tb.Fatalf("session type = %T, want *realtimeSession", session)
	}
	return realtime
}

func audioDeltaContentForTest(tb testing.TB, msg messages.StreamMessage) []byte {
	tb.Helper()
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok {
		tb.Fatalf("audio delta value type = %T", msg.Value)
	}
	return value.Content
}

func objectFieldForTest(tb testing.TB, object map[string]any, key string) map[string]any {
	tb.Helper()
	field, ok := object[key].(map[string]any)
	if !ok {
		tb.Fatalf("field %q = %T, want a JSON object", key, object[key])
	}
	return field
}
