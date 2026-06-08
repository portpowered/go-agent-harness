package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

type fakeCapabilityProvider struct {
	name        string
	capability  providers.ProviderCapabilities
	inferCalls  int
	streamCalls int
}

func newFakeCapabilityProvider(capability providers.StatelessCapabilities) *fakeCapabilityProvider {
	return &fakeCapabilityProvider{
		name: "fake-capability-provider",
		capability: providers.ProviderCapabilities{
			Provider:  "fake-capability-provider",
			Stateless: capability,
		},
	}
}

func (p *fakeCapabilityProvider) Name() string {
	return p.name
}

func (p *fakeCapabilityProvider) Capabilities() providers.ProviderCapabilities {
	return p.capability
}

func (p *fakeCapabilityProvider) Infer(context.Context, providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.inferCalls++
	return providers.InferenceResponse{
		Message: models.NewTextMessage(models.RoleAssistant, "ok"),
	}, nil
}

func (p *fakeCapabilityProvider) InferStream(context.Context, providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.streamCalls++
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

type fakeSessionCapabilityProvider struct {
	name         string
	capability   providers.ProviderCapabilities
	connectCalls int
}

func newFakeSessionCapabilityProvider(capability providers.SessionCapabilities) *fakeSessionCapabilityProvider {
	return &fakeSessionCapabilityProvider{
		name: "fake-capability-provider",
		capability: providers.ProviderCapabilities{
			Provider: "fake-capability-provider",
			Session:  &capability,
		},
	}
}

func (p *fakeSessionCapabilityProvider) Name() string {
	return p.name
}

func (p *fakeSessionCapabilityProvider) Capabilities() providers.ProviderCapabilities {
	return p.capability
}

func (p *fakeSessionCapabilityProvider) ConnectSession(context.Context, models.SessionConfig) (messages.Session, error) {
	p.connectCalls++
	return fakeSession{}, nil
}

type fakeSession struct{}

func (fakeSession) Send(context.Context, messages.StreamMessage) bool {
	return true
}

func (fakeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return nil
}

func (fakeSession) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (fakeSession) Close() error {
	return nil
}

func TestDefaultGatewayInferRejectsUnsupportedStatelessFeaturesBeforeProviderCall(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		feature    string
		capability func(*providers.StatelessCapabilities)
		request    InferenceRequest
	}{
		{
			name:    "tools",
			feature: "tools",
			capability: func(c *providers.StatelessCapabilities) {
				c.Tools = providers.Unsupported("tools disabled")
			},
			request: InferenceRequest{
				Messages: []models.Message{models.NewTextMessage(models.RoleUser, "hello")},
				Tools:    []models.ToolDefinition{{Name: "lookup"}},
			},
		},
		{
			name:    "image input",
			feature: "imageInput",
			capability: func(c *providers.StatelessCapabilities) {
				c.ImageInput = providers.Unsupported("images disabled")
			},
			request: InferenceRequest{Messages: []models.Message{{
				Role:         models.RoleUser,
				ContentParts: []models.ContentPart{models.ImagePart{URL: "https://example.test/image.png"}},
			}}},
		},
		{
			name:    "audio input",
			feature: "audioInput",
			capability: func(c *providers.StatelessCapabilities) {
				c.AudioInput = providers.Unsupported("audio disabled")
			},
			request: InferenceRequest{Messages: []models.Message{{
				Role:         models.RoleUser,
				ContentParts: []models.ContentPart{models.AudioPart{URL: "https://example.test/audio.mp3"}},
			}}},
		},
		{
			name:    "video input",
			feature: "videoInput",
			capability: func(c *providers.StatelessCapabilities) {
				c.VideoInput = providers.Unsupported("video disabled")
			},
			request: InferenceRequest{Messages: []models.Message{{
				Role:         models.RoleUser,
				ContentParts: []models.ContentPart{models.VideoPart{URL: "https://example.test/video.mp4"}},
			}}},
		},
		{
			name:    "reasoning option",
			feature: "reasoning",
			capability: func(c *providers.StatelessCapabilities) {
				c.Reasoning = providers.Unsupported("reasoning disabled")
			},
			request: InferenceRequest{
				Messages: []models.Message{models.NewTextMessage(models.RoleUser, "think")},
				Thinking: &providers.ThinkingConfig{Mode: providers.ThinkingEnabled, BudgetTokens: 1024},
			},
		},
		{
			name:    "reasoning content",
			feature: "reasoning",
			capability: func(c *providers.StatelessCapabilities) {
				c.Reasoning = providers.Unsupported("reasoning disabled")
			},
			request: InferenceRequest{Messages: []models.Message{
				messages.NewReasoningMessage(models.RoleAssistant, "private reasoning"),
			}},
		},
		{
			name:    "prompt caching",
			feature: "promptCaching",
			capability: func(c *providers.StatelessCapabilities) {
				c.PromptCaching = providers.Unsupported("cache control disabled")
			},
			request: InferenceRequest{
				Messages:     []models.Message{models.NewTextMessage(models.RoleUser, "cache this")},
				CacheControl: &providers.CacheControlConfig{CacheRetentionPolicy: providers.CacheRetention24h},
			},
		},
		{
			name:    "provider options",
			feature: "providerOptions",
			capability: func(c *providers.StatelessCapabilities) {
				c.ProviderOptions = providers.Unsupported("raw config disabled")
			},
			request: InferenceRequest{
				Messages: []models.Message{models.NewTextMessage(models.RoleUser, "configured")},
				Config:   json.RawMessage(`{"extra":true}`),
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			caps := supportedStatelessCapabilities()
			tc.capability(&caps)
			provider := newFakeCapabilityProvider(caps)
			gw, err := NewGateway(WithProvider(provider))
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}

			_, err = gw.Infer(context.Background(), tc.request)
			assertUnsupportedFeatureError(t, err, tc.feature)
			if provider.inferCalls != 0 {
				t.Fatalf("Infer calls = %d, want 0", provider.inferCalls)
			}
		})
	}
}

func TestDefaultGatewayInferStreamRejectsUnsupportedStreamingBeforeProviderCall(t *testing.T) {
	t.Parallel()

	caps := supportedStatelessCapabilities()
	caps.Streaming = providers.Unsupported("streaming disabled")
	provider := newFakeCapabilityProvider(caps)
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ch, err := gw.InferStream(context.Background(), InferenceRequest{
		Messages: []models.Message{models.NewTextMessage(models.RoleUser, "stream this")},
	})

	if ch != nil {
		t.Fatalf("InferStream channel = %#v, want nil on local rejection", ch)
	}
	assertUnsupportedFeatureError(t, err, "streaming")
	if provider.streamCalls != 0 {
		t.Fatalf("InferStream calls = %d, want 0", provider.streamCalls)
	}
}

func TestDefaultGatewayInteractRejectsUnsupportedStatelessFeaturesBeforeProviderCall(t *testing.T) {
	t.Parallel()

	caps := supportedStatelessCapabilities()
	caps.Tools = providers.Unsupported("tools disabled")
	provider := newFakeCapabilityProvider(caps)
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID: "unsupported-tools",
		Tools:         []InteractionTool{{Name: "lookup"}},
		Messages: []InteractionMessage{{
			Role:         InteractionRoleUser,
			ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "hello"}},
		}},
	})

	if provider.inferCalls != 0 {
		t.Fatalf("Infer calls = %d, want 0", provider.inferCalls)
	}
	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventError,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !equalInteractionEventTypes(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[1].Error == nil {
		t.Fatal("error event payload is nil")
	}
	if events[1].Error.Code != "unsupported_feature" {
		t.Fatalf("error code = %q, want unsupported_feature", events[1].Error.Code)
	}
	if string(events[1].Error.Details["feature"]) != `"tools"` {
		t.Fatalf("feature detail = %s, want tools", events[1].Error.Details["feature"])
	}
	if string(events[1].Error.Details["state"]) != `"unsupported"` {
		t.Fatalf("state detail = %s, want unsupported", events[1].Error.Details["state"])
	}
}

func TestDefaultSessionGatewayRejectsUnsupportedSessionFeaturesBeforeProviderCall(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		feature    string
		capability func(*providers.SessionCapabilities)
		config     models.SessionConfig
	}{
		{
			name:    "sessions",
			feature: "sessions",
			capability: func(c *providers.SessionCapabilities) {
				c.Sessions = providers.Unsupported("sessions disabled")
			},
			config: models.SessionConfig{Model: "realtime-model"},
		},
		{
			name:    "tools",
			feature: "tools",
			capability: func(c *providers.SessionCapabilities) {
				c.Tools = providers.Unsupported("session tools disabled")
			},
			config: models.SessionConfig{
				Model: "realtime-model",
				Tools: []models.ToolDefinition{{Name: "lookup"}},
			},
		},
		{
			name:    "text modality",
			feature: "textModality",
			capability: func(c *providers.SessionCapabilities) {
				c.TextModality = providers.Unsupported("text disabled")
			},
			config: models.SessionConfig{
				Model:      "realtime-model",
				Modalities: []models.SessionModality{models.SessionModalityText},
			},
		},
		{
			name:    "audio modality",
			feature: "audioModality",
			capability: func(c *providers.SessionCapabilities) {
				c.AudioModality = providers.Unsupported("audio disabled")
			},
			config: models.SessionConfig{
				Model:      "realtime-model",
				Modalities: []models.SessionModality{models.SessionModalityAudio},
			},
		},
		{
			name:    "input audio format",
			feature: "inputAudioFormat:pcm16",
			capability: func(c *providers.SessionCapabilities) {
				c.InputAudioFormats[models.AudioFormatPCM16] = providers.Unsupported("pcm16 input disabled")
			},
			config: models.SessionConfig{
				Model:            "realtime-model",
				InputAudioFormat: models.AudioFormatPCM16,
			},
		},
		{
			name:    "output audio format",
			feature: "outputAudioFormat:g711_ulaw",
			capability: func(c *providers.SessionCapabilities) {
				c.OutputAudioFormats[models.AudioFormatG711Ulaw] = providers.Unsupported("g711 ulaw output disabled")
			},
			config: models.SessionConfig{
				Model:             "realtime-model",
				OutputAudioFormat: models.AudioFormatG711Ulaw,
			},
		},
		{
			name:    "turn detection",
			feature: "turnDetection",
			capability: func(c *providers.SessionCapabilities) {
				c.TurnDetection = providers.Unsupported("turn detection disabled")
			},
			config: models.SessionConfig{
				Model:         "realtime-model",
				TurnDetection: &models.TurnDetectionConfig{Type: "server_vad"},
			},
		},
		{
			name:    "provider options",
			feature: "providerOptions",
			capability: func(c *providers.SessionCapabilities) {
				c.ProviderOptions = providers.Unsupported("raw config disabled")
			},
			config: models.SessionConfig{
				Model:  "realtime-model",
				Config: json.RawMessage(`{"extra":true}`),
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			caps := supportedSessionCapabilities()
			tc.capability(&caps)
			provider := newFakeSessionCapabilityProvider(caps)
			gw, err := NewSessionGateway(WithSessionProvider(provider))
			if err != nil {
				t.Fatalf("NewSessionGateway: %v", err)
			}

			session, err := gw.ConnectSession(context.Background(), tc.config)

			if session != nil {
				t.Fatalf("ConnectSession session = %#v, want nil on local rejection", session)
			}
			assertUnsupportedFeatureErrorWithMode(t, err, tc.feature, "session")
			if provider.connectCalls != 0 {
				t.Fatalf("ConnectSession calls = %d, want 0", provider.connectCalls)
			}
		})
	}
}

func TestDefaultSessionGatewayAllowsUnknownSessionCapabilitiesByContract(t *testing.T) {
	t.Parallel()

	caps := supportedSessionCapabilities()
	caps.InputAudioFormats[models.AudioFormatPCM16] = providers.Unknown("format support depends on model")
	provider := newFakeSessionCapabilityProvider(caps)
	gw, err := NewSessionGateway(WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}

	_, err = gw.ConnectSession(context.Background(), models.SessionConfig{
		Model:            "realtime-model",
		InputAudioFormat: models.AudioFormatPCM16,
	})

	if err != nil {
		t.Fatalf("ConnectSession() error = %v, want nil for unknown capability pass-through", err)
	}
	if provider.connectCalls != 1 {
		t.Fatalf("ConnectSession calls = %d, want 1", provider.connectCalls)
	}
}

func TestDefaultGatewayInferAllowsUnknownCapabilitiesByContract(t *testing.T) {
	t.Parallel()

	caps := supportedStatelessCapabilities()
	caps.VideoInput = providers.Unknown("video support depends on model")
	provider := newFakeCapabilityProvider(caps)
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	_, err = gw.Infer(context.Background(), InferenceRequest{Messages: []models.Message{{
		Role:         models.RoleUser,
		ContentParts: []models.ContentPart{models.VideoPart{URL: "https://example.test/video.mp4"}},
	}}})

	if err != nil {
		t.Fatalf("Infer() error = %v, want nil for unknown capability pass-through", err)
	}
	if provider.inferCalls != 1 {
		t.Fatalf("Infer calls = %d, want 1", provider.inferCalls)
	}
}

func equalInteractionEventTypes(a, b []InteractionEventType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func supportedStatelessCapabilities() providers.StatelessCapabilities {
	supported := providers.Supported()
	return providers.StatelessCapabilities{
		Inference:       supported,
		Streaming:       supported,
		Tools:           supported,
		ImageInput:      supported,
		AudioInput:      supported,
		VideoInput:      supported,
		VideoOutput:     supported,
		Reasoning:       supported,
		PromptCaching:   supported,
		ProviderOptions: supported,
	}
}

func supportedSessionCapabilities() providers.SessionCapabilities {
	supported := providers.Supported()
	return providers.SessionCapabilities{
		Sessions:           supported,
		Tools:              supported,
		TextModality:       supported,
		AudioModality:      supported,
		InputAudioFormats:  providers.RealtimeAudioFormats(),
		OutputAudioFormats: providers.RealtimeAudioFormats(),
		TurnDetection:      supported,
		ProviderOptions:    supported,
	}
}

func assertUnsupportedFeatureError(t *testing.T, err error, feature string) {
	t.Helper()

	assertUnsupportedFeatureErrorWithMode(t, err, feature, "stateless")
}

func assertUnsupportedFeatureErrorWithMode(t *testing.T, err error, feature, mode string) {
	t.Helper()

	var unsupported *providers.UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedFeatureError", err)
	}
	if unsupported.Provider != "fake-capability-provider" {
		t.Fatalf("Provider = %q", unsupported.Provider)
	}
	if unsupported.Feature != feature {
		t.Fatalf("Feature = %q, want %q", unsupported.Feature, feature)
	}
	if unsupported.Mode != mode {
		t.Fatalf("Mode = %q, want %s", unsupported.Mode, mode)
	}
	if unsupported.Capability.State != providers.CapabilityUnsupported {
		t.Fatalf("Capability.State = %q", unsupported.Capability.State)
	}
}
