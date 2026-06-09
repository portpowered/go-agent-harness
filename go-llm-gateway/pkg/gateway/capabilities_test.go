package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-llm-gateway/pkg/providers/fal"
)

type capabilityProvider struct {
	name        string
	caps        ProviderCapabilities
	capCalls    int
	inferCalls  int
	streamCalls int
}

func (p *capabilityProvider) Name() string {
	return p.name
}

func (p *capabilityProvider) Capabilities() ProviderCapabilities {
	p.capCalls++
	return p.caps
}

func (p *capabilityProvider) Infer(_ context.Context, _ providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.inferCalls++
	return providers.InferenceResponse{}, nil
}

func (p *capabilityProvider) InferStream(_ context.Context, _ providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.streamCalls++
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

type countingTransport struct {
	calls int
}

func (t *countingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
	}, nil
}

type countingFalProvider struct {
	*fal.FalProvider
	streamCalls int
}

func (p *countingFalProvider) InferStream(ctx context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.streamCalls++
	return p.FalProvider.InferStream(ctx, req)
}

type legacyProvider struct {
	name        string
	inferCalls  int
	streamCalls int
}

func (p *legacyProvider) Name() string {
	return p.name
}

func (p *legacyProvider) Infer(_ context.Context, _ providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.inferCalls++
	return providers.InferenceResponse{}, nil
}

func (p *legacyProvider) InferStream(_ context.Context, _ providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.streamCalls++
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

type capabilitySessionProvider struct {
	name         string
	caps         ProviderCapabilities
	capCalls     int
	connectCalls int
}

func (p *capabilitySessionProvider) Name() string {
	return p.name
}

func (p *capabilitySessionProvider) Capabilities() ProviderCapabilities {
	p.capCalls++
	return p.caps
}

func (p *capabilitySessionProvider) ConnectSession(context.Context, models.SessionConfig) (messages.Session, error) {
	p.connectCalls++
	return nil, nil
}

func TestGatewayCapabilitiesUsesProviderReporterWithoutInference(t *testing.T) {
	t.Parallel()

	provider := &capabilityProvider{
		name: "fake-provider",
		caps: ProviderCapabilities{
			Provider: "",
			Stateless: capabilities.StatelessCapabilities{
				Tools:     capabilities.Supported("test provider supports tools"),
				Streaming: capabilities.Unsupported("streaming disabled in test provider"),
			},
			Session: capabilities.SessionCapabilities{
				Sessions: capabilities.Unknown("session behavior not reported"),
			},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	got := gw.Capabilities()

	if got.Provider != "fake-provider" {
		t.Fatalf("Provider = %q, want fake-provider", got.Provider)
	}
	if got.Stateless.Tools.State != CapabilityStateSupported {
		t.Fatalf("Tools state = %q, want supported", got.Stateless.Tools.State)
	}
	if got.Stateless.Streaming.State != CapabilityStateUnsupported {
		t.Fatalf("Streaming state = %q, want unsupported", got.Stateless.Streaming.State)
	}
	if provider.capCalls != 1 {
		t.Fatalf("capability calls = %d, want 1", provider.capCalls)
	}
	if provider.inferCalls != 0 || provider.streamCalls != 0 {
		t.Fatalf("discovery called provider execution: infer=%d stream=%d", provider.inferCalls, provider.streamCalls)
	}
}

func TestGatewayCapabilitiesFallbacksToUnknownForLegacyProvider(t *testing.T) {
	t.Parallel()

	provider := &legacyProvider{name: "legacy-provider"}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	got := gw.Capabilities()

	if got.Provider != "legacy-provider" {
		t.Fatalf("Provider = %q, want legacy-provider", got.Provider)
	}
	if got.Stateless.Tools.State != CapabilityStateUnknown {
		t.Fatalf("Tools state = %q, want unknown", got.Stateless.Tools.State)
	}
	if got.Stateless.Streaming.State != CapabilityStateUnknown {
		t.Fatalf("Streaming state = %q, want unknown", got.Stateless.Streaming.State)
	}
	if got.Session.Sessions.State != CapabilityStateUnknown {
		t.Fatalf("Sessions state = %q, want unknown", got.Session.Sessions.State)
	}
	if provider.inferCalls != 0 || provider.streamCalls != 0 {
		t.Fatalf("discovery called provider execution: infer=%d stream=%d", provider.inferCalls, provider.streamCalls)
	}
}

func TestSessionGatewayCapabilitiesUsesProviderReporterWithoutConnecting(t *testing.T) {
	t.Parallel()

	provider := &capabilitySessionProvider{
		name: "session-provider",
		caps: ProviderCapabilities{
			Provider: "session-provider",
			Session: capabilities.SessionCapabilities{
				Sessions:    capabilities.Supported("session connection supported"),
				AudioInput:  capabilities.Supported("audio input supported"),
				AudioOutput: capabilities.Unsupported("audio output disabled in test provider"),
			},
		},
	}
	gw, err := NewSessionGateway(WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}

	got := gw.Capabilities()

	if got.Provider != "session-provider" {
		t.Fatalf("Provider = %q, want session-provider", got.Provider)
	}
	if got.Session.Sessions.State != CapabilityStateSupported {
		t.Fatalf("Sessions state = %q, want supported", got.Session.Sessions.State)
	}
	if got.Session.AudioOutput.State != CapabilityStateUnsupported {
		t.Fatalf("AudioOutput state = %q, want unsupported", got.Session.AudioOutput.State)
	}
	if provider.capCalls != 1 {
		t.Fatalf("capability calls = %d, want 1", provider.capCalls)
	}
	if provider.connectCalls != 0 {
		t.Fatalf("discovery connected session provider %d times", provider.connectCalls)
	}
}

func TestSessionGatewayRejectsUnsupportedSessionFeaturesBeforeProviderConnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		caps    capabilities.SessionCapabilities
		config  models.SessionConfig
		feature Feature
	}{
		{
			name: "sessions",
			caps: capabilities.SessionCapabilities{
				Sessions: capabilities.Unsupported("session transport unavailable"),
			},
			feature: FeatureSessions,
		},
		{
			name: "tools",
			caps: capabilities.SessionCapabilities{
				Tools: capabilities.Unsupported("session tools unavailable"),
			},
			config: models.SessionConfig{
				Tools: []models.ToolDefinition{{Name: "lookup"}},
			},
			feature: FeatureTools,
		},
		{
			name: "audio input format",
			caps: capabilities.SessionCapabilities{
				AudioInput: capabilities.Unsupported("session audio input unavailable"),
			},
			config: models.SessionConfig{
				InputAudioFormat: models.AudioFormatPCM16,
			},
			feature: FeatureAudioInput,
		},
		{
			name: "audio input sample rate",
			caps: capabilities.SessionCapabilities{
				AudioInput: capabilities.Unsupported("session audio input unavailable"),
			},
			config: models.SessionConfig{
				InputAudioSampleRate: models.SampleRate16000,
			},
			feature: FeatureAudioInput,
		},
		{
			name: "audio output modality",
			caps: capabilities.SessionCapabilities{
				AudioOutput: capabilities.Unsupported("session audio output unavailable"),
			},
			config: models.SessionConfig{
				Modalities: []models.SessionModality{models.SessionModalityText, models.SessionModalityAudio},
			},
			feature: FeatureAudioOutput,
		},
		{
			name: "audio output format",
			caps: capabilities.SessionCapabilities{
				AudioOutput: capabilities.Unsupported("session audio output unavailable"),
			},
			config: models.SessionConfig{
				OutputAudioFormat: models.AudioFormatPCM16,
			},
			feature: FeatureAudioOutput,
		},
		{
			name: "audio output voice",
			caps: capabilities.SessionCapabilities{
				AudioOutput: capabilities.Unsupported("session audio output unavailable"),
			},
			config: models.SessionConfig{
				Voice: "alloy",
			},
			feature: FeatureAudioOutput,
		},
		{
			name: "audio output sample rate",
			caps: capabilities.SessionCapabilities{
				AudioOutput: capabilities.Unsupported("session audio output unavailable"),
			},
			config: models.SessionConfig{
				OutputAudioSampleRate: models.SampleRate24000,
			},
			feature: FeatureAudioOutput,
		},
		{
			name: "provider config",
			caps: capabilities.SessionCapabilities{
				ProviderSpecificConfig: capabilities.Unsupported("session raw config unavailable"),
			},
			config: models.SessionConfig{
				Config: json.RawMessage(`{"vendor":"specific"}`),
			},
			feature: FeatureProviderSpecificConfig,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &capabilitySessionProvider{
				name: "session-validation-provider",
				caps: ProviderCapabilities{
					Provider: "session-validation-provider",
					Session:  tt.caps,
				},
			}
			gw, err := NewSessionGateway(WithSessionProvider(provider))
			if err != nil {
				t.Fatalf("NewSessionGateway: %v", err)
			}

			_, err = gw.ConnectSession(context.Background(), tt.config)

			var unsupported *UnsupportedFeatureError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %v, want UnsupportedFeatureError", err)
			}
			if unsupported.Provider != "session-validation-provider" {
				t.Fatalf("provider = %q, want session-validation-provider", unsupported.Provider)
			}
			if unsupported.Feature != tt.feature {
				t.Fatalf("feature = %q, want %q", unsupported.Feature, tt.feature)
			}
			if unsupported.RequestedMode != capabilities.RequestedModeSession {
				t.Fatalf("mode = %q, want %q", unsupported.RequestedMode, capabilities.RequestedModeSession)
			}
			if unsupported.Capability.State != CapabilityStateUnsupported {
				t.Fatalf("capability state = %q, want unsupported", unsupported.Capability.State)
			}
			if provider.connectCalls != 0 {
				t.Fatalf("validation connected session provider %d times", provider.connectCalls)
			}
		})
	}
}

func TestSessionGatewayReturnsContextErrorBeforeUnsupportedFeatureValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{
			name: "canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			ctx: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			}(),
			want: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &capabilitySessionProvider{
				name: "session-validation-provider",
				caps: ProviderCapabilities{
					Provider: "session-validation-provider",
					Session: capabilities.SessionCapabilities{
						Sessions: capabilities.Unsupported("session transport unavailable"),
					},
				},
			}
			gw, err := NewSessionGateway(WithSessionProvider(provider))
			if err != nil {
				t.Fatalf("NewSessionGateway: %v", err)
			}

			_, err = gw.ConnectSession(tt.ctx, models.SessionConfig{})

			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			var unsupported *UnsupportedFeatureError
			if errors.As(err, &unsupported) {
				t.Fatalf("error = %v, did not want UnsupportedFeatureError", err)
			}
			if provider.capCalls != 0 {
				t.Fatalf("validation read capabilities after context finished %d times", provider.capCalls)
			}
			if provider.connectCalls != 0 {
				t.Fatalf("validation connected session provider %d times", provider.connectCalls)
			}
		})
	}
}

func TestSessionGatewayAllowsUnknownCapabilitiesWithoutClaimingSupport(t *testing.T) {
	t.Parallel()

	provider := &capabilitySessionProvider{
		name: "unknown-session-provider",
		caps: providers.UnknownProviderCapabilities("unknown-session-provider"),
	}
	gw, err := NewSessionGateway(WithSessionProvider(provider))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}

	_, err = gw.ConnectSession(context.Background(), models.SessionConfig{
		Modalities:            []models.SessionModality{models.SessionModalityAudio},
		InputAudioFormat:      models.AudioFormatPCM16,
		InputAudioSampleRate:  models.SampleRate16000,
		OutputAudioFormat:     models.AudioFormatPCM16,
		OutputAudioSampleRate: models.SampleRate24000,
		Tools:                 []models.ToolDefinition{{Name: "lookup"}},
		Config:                json.RawMessage(`{"vendor":"specific"}`),
	})
	if err != nil {
		t.Fatalf("ConnectSession with unknown capabilities: %v", err)
	}
	if provider.connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", provider.connectCalls)
	}

	got := gw.Capabilities()
	if got.Session.Sessions.IsSupported() {
		t.Fatalf("unknown sessions capability must not report support")
	}
}

func TestGatewayRejectsUnsupportedStatelessFeaturesBeforeProviderCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		caps    capabilities.StatelessCapabilities
		req     InferenceRequest
		stream  bool
		feature Feature
		mode    string
	}{
		{
			name: "tools",
			caps: capabilities.StatelessCapabilities{
				Tools: capabilities.Unsupported("tools unavailable"),
			},
			req: InferenceRequest{
				Tools: []models.ToolDefinition{{Name: "lookup"}},
			},
			feature: FeatureTools,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "streaming",
			caps: capabilities.StatelessCapabilities{
				Streaming: capabilities.Unsupported("stream API unavailable"),
			},
			stream:  true,
			feature: FeatureStreaming,
			mode:    capabilities.RequestedModeStatelessStream,
		},
		{
			name: "image input",
			caps: capabilities.StatelessCapabilities{
				ImageInput: capabilities.Unsupported("image input unavailable"),
			},
			req: InferenceRequest{
				Messages: []models.Message{{
					Role:         models.RoleUser,
					ContentParts: []models.ContentPart{models.ImagePart{URL: "https://example.com/image.png"}},
				}},
			},
			feature: FeatureImageInput,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "audio input",
			caps: capabilities.StatelessCapabilities{
				AudioInput: capabilities.Unsupported("audio input unavailable"),
			},
			req: InferenceRequest{
				Messages: []models.Message{{
					Role:         models.RoleUser,
					ContentParts: []models.ContentPart{models.AudioPart{URL: "https://example.com/audio.mp3"}},
				}},
			},
			feature: FeatureAudioInput,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "audio output in history",
			caps: capabilities.StatelessCapabilities{
				AudioOutput: capabilities.Unsupported("audio output unavailable"),
			},
			req: InferenceRequest{
				Messages: []models.Message{{
					Role:         models.RoleAssistant,
					ContentParts: []models.ContentPart{models.AudioPart{Bytes: []byte("wav"), MediaType: "audio/wav"}},
				}},
			},
			feature: FeatureAudioOutput,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "video output in history",
			caps: capabilities.StatelessCapabilities{
				VideoOutput: capabilities.Unsupported("video output unavailable"),
			},
			req: InferenceRequest{
				Messages: []models.Message{{
					Role:         models.RoleAssistant,
					ContentParts: []models.ContentPart{models.VideoPart{URL: "https://example.com/video.mp4"}},
				}},
			},
			feature: FeatureVideoOutput,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "reasoning",
			caps: capabilities.StatelessCapabilities{
				Reasoning: capabilities.Unsupported("reasoning unavailable"),
			},
			req: InferenceRequest{
				Thinking: &providers.ThinkingConfig{Mode: providers.ThinkingEnabled, BudgetTokens: 4096},
			},
			feature: FeatureReasoning,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "prompt caching",
			caps: capabilities.StatelessCapabilities{
				PromptCaching: capabilities.Unsupported("prompt caching unavailable"),
			},
			req: InferenceRequest{
				CacheControl: &providers.CacheControlConfig{CacheRetentionPolicy: providers.CacheRetentionInMemory},
			},
			feature: FeaturePromptCaching,
			mode:    capabilities.RequestedModeStateless,
		},
		{
			name: "provider config",
			caps: capabilities.StatelessCapabilities{
				ProviderSpecificConfig: capabilities.Unsupported("raw config unavailable"),
			},
			req: InferenceRequest{
				Config: json.RawMessage(`{"duration":"5s"}`),
			},
			feature: FeatureProviderSpecificConfig,
			mode:    capabilities.RequestedModeStateless,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &capabilityProvider{
				name: "validation-provider",
				caps: ProviderCapabilities{
					Provider:  "validation-provider",
					Stateless: tt.caps,
				},
			}
			gw, err := NewGateway(WithProvider(provider))
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}

			if tt.stream {
				_, err = gw.InferStream(context.Background(), tt.req)
			} else {
				_, err = gw.Infer(context.Background(), tt.req)
			}

			var unsupported *UnsupportedFeatureError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %v, want UnsupportedFeatureError", err)
			}
			if unsupported.Provider != "validation-provider" {
				t.Fatalf("provider = %q, want validation-provider", unsupported.Provider)
			}
			if unsupported.Feature != tt.feature {
				t.Fatalf("feature = %q, want %q", unsupported.Feature, tt.feature)
			}
			if unsupported.RequestedMode != tt.mode {
				t.Fatalf("mode = %q, want %q", unsupported.RequestedMode, tt.mode)
			}
			if unsupported.Capability.State != CapabilityStateUnsupported {
				t.Fatalf("capability state = %q, want unsupported", unsupported.Capability.State)
			}
			if provider.inferCalls != 0 || provider.streamCalls != 0 {
				t.Fatalf("validation called provider execution: infer=%d stream=%d", provider.inferCalls, provider.streamCalls)
			}
		})
	}
}

func TestGatewayRejectsFalStreamingBeforeProviderExecution(t *testing.T) {
	t.Parallel()

	transport := &countingTransport{}
	provider := &countingFalProvider{
		FalProvider: fal.New(fal.WithHTTPClient(&http.Client{Transport: transport})),
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ch, err := gw.InferStream(context.Background(), InferenceRequest{
		Model: fal.ModelLTXAudioToVideo,
		Messages: []models.Message{{
			Role: models.RoleUser,
			ContentParts: []models.ContentPart{
				models.TextPart{Text: "render"},
				models.AudioPart{URL: "https://example.com/audio.mp3"},
			},
		}},
	})
	if ch != nil {
		t.Fatalf("InferStream() channel = %#v, want nil", ch)
	}
	var unsupported *UnsupportedFeatureError
	if !errors.As(err, &unsupported) {
		t.Fatalf("InferStream() error = %v, want UnsupportedFeatureError", err)
	}
	if unsupported.Provider != "fal" {
		t.Fatalf("provider = %q, want fal", unsupported.Provider)
	}
	if unsupported.Feature != FeatureStreaming {
		t.Fatalf("feature = %q, want streaming", unsupported.Feature)
	}
	if unsupported.RequestedMode != capabilities.RequestedModeStatelessStream {
		t.Fatalf("mode = %q, want stateless_stream", unsupported.RequestedMode)
	}
	if unsupported.Capability.State != CapabilityStateUnsupported {
		t.Fatalf("capability state = %q, want unsupported", unsupported.Capability.State)
	}
	if provider.streamCalls != 0 {
		t.Fatalf("gateway called fal InferStream before rejecting: %d", provider.streamCalls)
	}
	if transport.calls != 0 {
		t.Fatalf("gateway attempted fal HTTP before rejecting: %d", transport.calls)
	}
}

func TestGatewayAllowsUnknownCapabilitiesWithoutClaimingSupport(t *testing.T) {
	t.Parallel()

	provider := &capabilityProvider{
		name: "unknown-provider",
		caps: providers.UnknownProviderCapabilities("unknown-provider"),
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	_, err = gw.Infer(context.Background(), InferenceRequest{
		Tools: []models.ToolDefinition{{Name: "lookup"}},
		Messages: []models.Message{{
			Role:         models.RoleUser,
			ContentParts: []models.ContentPart{models.ImagePart{URL: "https://example.com/image.png"}},
		}},
		Thinking:     &providers.ThinkingConfig{Mode: providers.ThinkingAdaptive},
		CacheControl: &providers.CacheControlConfig{},
		Config:       json.RawMessage(`{"vendor":"specific"}`),
	})
	if err != nil {
		t.Fatalf("Infer with unknown capabilities: %v", err)
	}
	if provider.inferCalls != 1 {
		t.Fatalf("infer calls = %d, want 1", provider.inferCalls)
	}

	_, err = gw.InferStream(context.Background(), InferenceRequest{})
	if err != nil {
		t.Fatalf("InferStream with unknown capabilities: %v", err)
	}
	if provider.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", provider.streamCalls)
	}

	got := gw.Capabilities()
	if got.Stateless.Tools.IsSupported() {
		t.Fatalf("unknown tools capability must not report support")
	}
	if got.Stateless.Streaming.IsSupported() {
		t.Fatalf("unknown streaming capability must not report support")
	}
}
