package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

type literalCatalog struct{}

func (literalCatalog) RealtimeModels(provider string) []providers.RealtimeModel {
	if strings.ToLower(strings.TrimSpace(provider)) != "openai" {
		return nil
	}
	return []providers.RealtimeModel{
		{ID: "gpt-realtime", SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true},
		{ID: "gpt-realtime-2.1-mini", SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true},
		{ID: "gpt-realtime-2.1", SupportsAudio: true, SupportsImageInput: true, SupportsFunctionCalling: true, SupportsReasoning: true},
	}
}

func (literalCatalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	for _, candidate := range (literalCatalog{}).RealtimeModels(provider) {
		if candidate.ID == model {
			return candidate, true
		}
	}
	return providers.RealtimeModel{}, false
}

func (literalCatalog) SupportedRealtimeModelIDs(provider string) []string {
	models := (literalCatalog{}).RealtimeModels(provider)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestIndependentLiteralTransportMatrix(t *testing.T) {
	tests := []struct {
		name      string
		request   sessionconfig.Request
		transport string
		signaling string
		media     string
		fields    []string
		causes    []error
		text      string
	}{
		{name: "omitted defaults to websocket", request: sessionconfig.Request{}, transport: "ws"},
		{name: "explicit websocket", request: sessionconfig.Request{Transport: " WS "}, transport: "ws"},
		{
			name: "explicit webrtc with equal aliases",
			request: sessionconfig.Request{
				Transport: " WebRTC ", Signaling: "loopback://same", SignalingEndpoint: "loopback://same", MediaSource: "fixture://media",
			},
			transport: "webrtc", signaling: "loopback://same", media: "fixture://media",
		},
		{name: "unknown transport", request: sessionconfig.Request{Transport: "quic"}, fields: []string{"transport"}, causes: []error{sessionconfig.ErrInvalidSessionTransport}, text: `invalid session transport: "quic"`},
		{name: "websocket signaling", request: sessionconfig.Request{Signaling: "loopback"}, fields: []string{"transport", "signaling"}, causes: []error{sessionconfig.ErrSessionSignalingRequiresWebRTC}, text: "session signaling requires WebRTC transport"},
		{name: "websocket media", request: sessionconfig.Request{MediaSource: "fixture"}, fields: []string{"transport", "media-source"}, causes: []error{sessionconfig.ErrSessionMediaSourceRequiresWebRTC}, text: "session media source requires WebRTC transport"},
		{name: "webrtc prerequisites", request: sessionconfig.Request{Transport: "webrtc"}, fields: []string{"transport", "signaling", "media-source"}, causes: []error{sessionconfig.ErrSessionWebRTCRequiresSignaling, sessionconfig.ErrSessionWebRTCRequiresMediaSource}, text: "WebRTC session transport requires signaling"},
		{name: "alias conflict", request: sessionconfig.Request{Transport: "webrtc", Signaling: "loopback://one", SignalingEndpoint: "loopback://two", MediaSource: "fixture://media"}, fields: []string{"signaling", "signaling-endpoint"}, causes: []error{sessionconfig.ErrSessionRuntimeSelectionConflict}, text: "conflicting session signaling endpoints"},
	}

	service := New(literalCatalog{})
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := service.ResolveRuntimeSelection(testCase.request)
			if len(testCase.fields) == 0 {
				if err != nil || got.Transport != testCase.transport || got.SignalingEndpoint != testCase.signaling || got.MediaSource != testCase.media {
					t.Fatalf("selection = %#v, error = %v; want %q/%q/%q", got, err, testCase.transport, testCase.signaling, testCase.media)
				}
				return
			}
			assertSelectionError(t, err, testCase.fields, testCase.causes, testCase.text)
		})
	}
}

func assertSelectionError(t *testing.T, err error, fields []string, causes []error, text string) {
	t.Helper()
	if err == nil || !errors.Is(err, sessionconfig.ErrInvalidSessionRuntimeSelection) {
		t.Fatalf("error = %v, want typed runtime selection error", err)
	}
	var typed *sessionconfig.RuntimeSelectionError
	if !errors.As(err, &typed) || !reflect.DeepEqual(typedFields(typed), fields) {
		t.Fatalf("error = %v, fields = %v, want %v", err, typedFields(typed), fields)
	}
	for _, cause := range causes {
		if !errors.Is(err, cause) {
			t.Fatalf("error = %v, want cause %v", err, cause)
		}
	}
	if !strings.Contains(err.Error(), text) {
		t.Fatalf("error = %q, want %q", err, text)
	}
}

func typedFields(err *sessionconfig.RuntimeSelectionError) []string {
	if err == nil {
		return nil
	}
	return err.Fields
}

func TestIndependentLiteralCaptureMatrix(t *testing.T) {
	tests := []struct {
		name    string
		request sessionconfig.Request
		want    string
	}{
		{name: "empty", request: sessionconfig.Request{}},
		{name: "record json", request: sessionconfig.Request{RecordPath: "capture.json"}},
		{name: "replay json", request: sessionconfig.Request{ReplayPath: "replay.JSON"}},
		{name: "recorded timing", request: sessionconfig.Request{ReplayPath: "replay.json", ReplayTiming: "RECORDED"}},
		{name: "record replay conflict", request: sessionconfig.Request{RecordPath: "capture.json", ReplayPath: "replay.json"}, want: "agent session does not support --record and --replay together; choose one capture mode"},
		{name: "record extension", request: sessionconfig.Request{RecordPath: "capture.txt"}, want: `--record path "capture.txt" must end with .json`},
		{name: "replay extension", request: sessionconfig.Request{ReplayPath: "replay.txt"}, want: `--replay path "replay.txt" must end with .json`},
		{name: "recorded without replay", request: sessionconfig.Request{ReplayTiming: "recorded"}, want: "--replay-timing recorded requires --replay"},
		{name: "unknown timing", request: sessionconfig.Request{ReplayTiming: "elastic"}, want: `--replay-timing "elastic" is invalid; use immediate or recorded`},
	}

	service := New(literalCatalog{})
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := service.ValidateCapture(testCase.request)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("ValidateCapture() = %v, want success", err)
				}
				return
			}
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("ValidateCapture() = %v, want exact %q", err, testCase.want)
			}
		})
	}
	if service.NormalizeReplayTiming("  RECORDED ") != "recorded" || service.NormalizeReplayTiming("invalid") != "" {
		t.Fatal("replay timing normalization diverged from literal matrix")
	}
}

func TestIndependentLiteralProviderDefaults(t *testing.T) {
	service := New(literalCatalog{})
	defaultConfig, err := service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Defaults: sessionconfig.Defaults{Provider: "openrouter", OpenAI: &sessionconfig.ProviderConfig{APIKey: "synthetic-key"}}})
	if err != nil || defaultConfig.Model != "gpt-realtime-2.1-mini" {
		t.Fatalf("omitted provider/model = %+v, %v; want literal realtime default", defaultConfig, err)
	}
	explicitConfig, err := service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Provider: "OPENAI", ProviderProvided: true, Model: "gpt-realtime-2.1", ModelProvided: true, APIKey: "override-key", BaseURL: "wss://override.test", Defaults: sessionconfig.Defaults{Provider: "grok", OpenAI: &sessionconfig.ProviderConfig{APIKey: "synthetic-key"}}})
	if err != nil || explicitConfig.Model != "gpt-realtime-2.1" || explicitConfig.APIKey != "override-key" || explicitConfig.BaseURL != "wss://override.test" {
		t.Fatalf("explicit provider/model = %+v, %v; want literal overrides", explicitConfig, err)
	}

	for _, testCase := range []struct {
		name     string
		request  sessionconfig.Request
		provider string
	}{
		{
			name: "omitted provider uses session default",
			request: sessionconfig.Request{Defaults: sessionconfig.Defaults{
				Session: &sessionconfig.SessionDefaults{Provider: " GROK "},
			}},
			provider: sessionconfig.ProviderGrok,
		},
		{
			name: "explicit provider is normalized",
			request: sessionconfig.Request{Provider: " OPENAI ", ProviderProvided: true, Defaults: sessionconfig.Defaults{
				Provider: sessionconfig.ProviderGrok,
			}},
			provider: sessionconfig.ProviderOpenAI,
		},
		{
			name: "explicit empty provider does not use defaults",
			request: sessionconfig.Request{ProviderProvided: true, Defaults: sessionconfig.Defaults{
				Provider: sessionconfig.ProviderGrok,
				Session:  &sessionconfig.SessionDefaults{Provider: sessionconfig.ProviderGrok},
			}},
			provider: "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := service.ResolveProvider(testCase.request); got != testCase.provider {
				t.Fatalf("ResolveProvider() = %q, want %q", got, testCase.provider)
			}
		})
	}
}

func TestIndependentLiteralProviderRejections(t *testing.T) {
	service := New(literalCatalog{})
	defaults := sessionconfig.Defaults{Provider: "openai", OpenAI: &sessionconfig.ProviderConfig{APIKey: "synthetic-key"}}
	_, err := service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{ModelProvided: true, Defaults: defaults})
	var unsupported *providers.UnsupportedRealtimeModelError
	if err == nil || !errors.Is(err, providers.ErrUnsupportedRealtimeModel) || !errors.As(err, &unsupported) || unsupported.Model != "" {
		t.Fatalf("explicit empty model = %v, want typed empty-model rejection", err)
	}
	_, err = service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Model: "unknown", Defaults: defaults})
	if err == nil || !errors.As(err, &unsupported) || unsupported.Model != "unknown" {
		t.Fatalf("unknown model = %v, want typed raw-model rejection", err)
	}
	_, err = service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Defaults: sessionconfig.Defaults{Provider: "openai"}})
	if err == nil || !errors.Is(err, sessionconfig.ErrOpenAIRealtimeAPIKeyMissing) {
		t.Fatalf("missing credential = %v, want exact OpenAI sentinel", err)
	}
	_, err = service.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Model: "gpt-realtime-2.1-mini", ReasoningEffort: "low", Defaults: defaults})
	if err == nil || !strings.Contains(err.Error(), `OpenAI model "gpt-realtime-2.1-mini" does not support --reasoning-effort`) {
		t.Fatalf("unsupported reasoning = %v, want capability rejection", err)
	}
}

func TestIndependentLiteralGrokResolution(t *testing.T) {
	service := New(literalCatalog{})
	tests := []struct {
		name        string
		request     sessionconfig.Request
		model       string
		apiKey      string
		baseURL     string
		expectedErr string
	}{
		{
			name: "defaults are copied",
			request: sessionconfig.Request{Defaults: sessionconfig.Defaults{
				Provider: sessionconfig.ProviderGrok,
				Grok:     &sessionconfig.ProviderConfig{Model: "grok-test", APIKey: "xai-test", BaseURL: "wss://grok.example.test"},
			}},
			model:   "grok-test",
			apiKey:  "xai-test",
			baseURL: "wss://grok.example.test",
		},
		{
			name: "explicit model overrides defaults",
			request: sessionconfig.Request{Provider: sessionconfig.ProviderGrok, ProviderProvided: true, Model: "grok-explicit", ModelProvided: true, Defaults: sessionconfig.Defaults{
				Grok: &sessionconfig.ProviderConfig{Model: "grok-default", APIKey: "xai-test"},
			}},
			model:  "grok-explicit",
			apiKey: "xai-test",
		},
		{
			name: "provider mismatch is rejected",
			request: sessionconfig.Request{Provider: sessionconfig.ProviderOpenAI, ProviderProvided: true, Defaults: sessionconfig.Defaults{
				Grok: &sessionconfig.ProviderConfig{Model: "grok-test", APIKey: "xai-test"},
			}},
			expectedErr: `--record supports provider "grok" only; got "openai"`,
		},
		{
			name: "explicit empty provider is not defaulted",
			request: sessionconfig.Request{ProviderProvided: true, Defaults: sessionconfig.Defaults{
				Provider: sessionconfig.ProviderGrok,
				Grok:     &sessionconfig.ProviderConfig{Model: "grok-default", APIKey: "xai-test"},
			}},
			expectedErr: "--record requires --provider grok or --provider openai for live session inference",
		},
		{
			name: "explicit empty model is not defaulted",
			request: sessionconfig.Request{Provider: sessionconfig.ProviderGrok, ProviderProvided: true, ModelProvided: true, Defaults: sessionconfig.Defaults{
				Grok: &sessionconfig.ProviderConfig{Model: "grok-default", APIKey: "xai-test"},
			}},
			expectedErr: "grok session model is required for live session record mode",
		},
		{
			name: "omitted model remains required",
			request: sessionconfig.Request{Defaults: sessionconfig.Defaults{
				Provider: sessionconfig.ProviderGrok,
				Grok:     &sessionconfig.ProviderConfig{APIKey: "xai-test"},
			}},
			expectedErr: "grok session model is required for live session record mode",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := service.ResolveGrokConfig(testCase.request)
			if testCase.expectedErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.expectedErr) {
					t.Fatalf("Grok config = %+v, error = %v; want error containing %q", got, err, testCase.expectedErr)
				}
				return
			}
			if err != nil || got.Model != testCase.model || got.APIKey != testCase.apiKey || got.BaseURL != testCase.baseURL {
				t.Fatalf("Grok config = %+v, %v; want literal values model=%q api_key=%q base_url=%q", got, err, testCase.model, testCase.apiKey, testCase.baseURL)
			}
		})
	}
}

func TestIndependentLiteralCloneAndCapabilities(t *testing.T) {
	service := New(literalCatalog{})
	createResponse := true
	interruptResponse := false
	input := &models.TurnDetectionConfig{Type: "server_vad", CreateResponse: &createResponse, InterruptResponse: &interruptResponse}
	clone := service.CloneTurnDetection(input)
	if clone == nil || clone == input || clone.CreateResponse == input.CreateResponse || clone.InterruptResponse == input.InterruptResponse {
		t.Fatal("turn detection was not deeply cloned")
	}
	*clone.CreateResponse = false
	if !*input.CreateResponse || service.CloneTurnDetection(nil) != nil {
		t.Fatal("clone mutated caller input or mishandled nil")
	}
	wantVoices := []string{"alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage", "shimmer", "verse"}
	voices := service.SupportedOpenAIRealtimeVoices()
	if !reflect.DeepEqual(voices, wantVoices) {
		t.Fatalf("voices = %v, want %v", voices, wantVoices)
	}
	voices[0] = "mutated"
	if service.SupportedOpenAIRealtimeVoices()[0] != "alloy" {
		t.Fatal("voice registry leaked mutable storage")
	}
	if got := service.OpenAIRealtimeURL(sessionconfig.ProviderConfig{Model: "gpt-realtime-2.1"}); got != "wss://api.openai.com/v1/realtime?model=gpt-realtime-2.1" {
		t.Fatalf("realtime URL = %q, want literal normalized URL", got)
	}
	if got := service.OpenAIRealtimeURL(sessionconfig.ProviderConfig{Model: "new", BaseURL: "wss://example.test/realtime?model=existing"}); got != "wss://example.test/realtime?model=existing" {
		t.Fatalf("existing model URL = %q, want caller query preserved", got)
	}
	if got := service.OpenAIRealtimeURL(sessionconfig.ProviderConfig{Model: "new", BaseURL: "%%%"}); got != "%%%" {
		t.Fatalf("malformed URL fallback = %q, want unchanged input", got)
	}
}

func TestPolicyValidationAndNilCatalog(t *testing.T) {
	service := New(literalCatalog{})
	for _, voice := range []string{"", "alloy", "fable"} {
		if err := service.ValidateOpenAIRealtimeVoice(voice); err != nil {
			continue
		}
	}
	for _, effort := range []string{"", "minimal", "low", "medium", "high", "xhigh", "invalid"} {
		if err := service.ValidateOpenAIRealtimeReasoningEffort(effort); err != nil {
			continue
		}
	}
	if err := service.Validate(sessionconfig.Request{}); err != nil {
		t.Fatalf("empty full validation = %v", err)
	}
	if err := service.Validate(sessionconfig.Request{ReplayPath: "missing.json"}); err == nil || !strings.Contains(err.Error(), "replay session capture") {
		t.Fatal("missing replay was accepted")
	}

	var nilService *Service
	_, err := nilService.ResolveOpenAIRealtimeConfig(sessionconfig.Request{Model: "unknown", Defaults: sessionconfig.Defaults{OpenAI: &sessionconfig.ProviderConfig{APIKey: "key"}}})
	if !errors.Is(err, providers.ErrModelCatalogRequired) {
		t.Fatalf("nil catalog error = %v, want ErrModelCatalogRequired", err)
	}
	if got := nilService.SupportedOpenAIRealtimeVoices(); len(got) != 10 {
		t.Fatalf("nil receiver voice count = %d, want 10", len(got))
	}
}
