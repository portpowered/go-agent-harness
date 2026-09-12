package agentruntime

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestResolveRealtimeSessionProviderPrecedenceAndNormalization(t *testing.T) {
	tests := []struct {
		name     string
		opts     SessionRunOptions
		config   *config.Config
		provider string
	}{
		{
			name:     "explicit CLI provider wins",
			opts:     SessionRunOptions{ModelCatalog: testModelCatalog(), Provider: "  GROK  "},
			config:   &config.Config{Session: &config.SessionConfig{Provider: config.ProviderOpenAI}, Model: config.ModelConfig{Provider: config.ProviderOpenAI}},
			provider: config.ProviderGrok,
		},
		{
			name:     "session provider wins over model provider",
			config:   &config.Config{Session: &config.SessionConfig{Provider: "  OPENAI  "}, Model: config.ModelConfig{Provider: config.ProviderGrok}},
			provider: config.ProviderOpenAI,
		},
		{
			name:     "supported model provider",
			config:   &config.Config{Model: config.ModelConfig{Provider: "  GROK  "}},
			provider: config.ProviderGrok,
		},
		{
			name:     "generic model provider falls back to OpenAI",
			config:   &config.Config{Model: config.ModelConfig{Provider: "  OPENROUTER  "}},
			provider: config.ProviderOpenAI,
		},
		{
			name:     "missing model provider falls back to OpenAI",
			config:   &config.Config{},
			provider: config.ProviderOpenAI,
		},
		{
			name:     "unsupported explicit provider remains observable",
			opts:     SessionRunOptions{ModelCatalog: testModelCatalog(), Provider: "  OpenRouter  "},
			provider: config.ProviderOpenRouter,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resolveRealtimeSessionProvider(testCase.opts, testCase.config); got != testCase.provider {
				t.Fatalf("resolveRealtimeSessionProvider() = %q, want %q", got, testCase.provider)
			}
		})
	}
}

func TestResolveBareSessionOptionsUsesBareOpenAIDefaults(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fallback-key")
	loaded := &config.Config{
		ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
		Model: config.ModelConfig{
			Provider: config.DefaultModelProvider,
			OpenRouter: &config.OpenAIConfig{
				Model: config.DefaultModelModel,
			},
		},
	}

	resolved, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), LoadedConfig: loaded})
	if err != nil {
		t.Fatalf("ResolveBareSessionOptions(): %v", err)
	}
	if resolved.Provider != sessionProviderOpenAI {
		t.Fatalf("provider = %q, want %q", resolved.Provider, sessionProviderOpenAI)
	}
	if resolved.Model != openAIRealtimeModel {
		t.Fatalf("model = %q, want %q", resolved.Model, openAIRealtimeModel)
	}
	if resolved.Transport != SessionTransportWebSocket {
		t.Fatalf("transport = %q, want %q", resolved.Transport, SessionTransportWebSocket)
	}
	if !resolved.BareLive {
		t.Fatal("BareLive = false, want true")
	}
	if !resolved.RTCDeviceBinding.InputPresent || !resolved.RTCDeviceBinding.OutputPresent {
		t.Fatalf("bare device presence = %#v, want both directions present", resolved.RTCDeviceBinding)
	}
	if resolved.APIKey != "fallback-key" {
		t.Fatalf("API key = %q, want conventional environment fallback", resolved.APIKey)
	}
	if resolved.TurnDetection == nil || resolved.TurnDetection.Type != "semantic_vad" {
		t.Fatalf("turn detection = %#v, want semantic_vad", resolved.TurnDetection)
	}
	if resolved.InputAudioTranscription == nil || !resolved.InputAudioTranscription.Enabled || resolved.InputAudioTranscription.Model != models.DefaultInputAudioTranscriptionModel {
		t.Fatalf("input transcription = %#v, want enabled default", resolved.InputAudioTranscription)
	}
}

func TestResolveBareSessionOptionsKeepsGrokServerVADDefault(t *testing.T) {
	loaded := &config.Config{
		ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
		Model: config.ModelConfig{Provider: config.ProviderGrok, Grok: &config.GrokConfig{
			Model: "grok-voice", APIKey: "grok-key",
		}},
	}
	resolved, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), LoadedConfig: loaded})
	if err != nil {
		t.Fatalf("ResolveBareSessionOptions(): %v", err)
	}
	if got := resolved.TurnDetection; got == nil || got.Type != "server_vad" {
		t.Fatalf("turn detection = %#v, want Grok server_vad default", got)
	}
}

func TestResolveBareSessionOptionsHonorsSemanticVADPolicy(t *testing.T) {
	interruptResponse := true
	loaded := &config.Config{
		ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
		Model: config.ModelConfig{Provider: config.ProviderOpenAI, OpenAI: &config.OpenAIConfig{
			Model: "gpt-realtime", APIKey: "persisted-key",
		}},
		Session: &config.SessionConfig{VAD: &config.SessionVADConfig{
			Type: "SEMANTIC_VAD", Eagerness: "LOW", InterruptResponse: &interruptResponse,
		}},
	}

	resolved, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), LoadedConfig: loaded})
	if err != nil {
		t.Fatalf("ResolveBareSessionOptions(): %v", err)
	}
	if got := resolved.TurnDetection; got == nil || got.Type != "semantic_vad" || got.Eagerness != "low" || got.InterruptResponse == nil || !*got.InterruptResponse {
		t.Fatalf("turn detection = %#v, want normalized semantic policy", got)
	}
}

func TestResolveBareSessionOptionsRejectsIncompatibleVADFields(t *testing.T) {
	base := func(vad *config.SessionVADConfig) SessionRunOptions {
		return SessionRunOptions{ModelCatalog: testModelCatalog(), LoadedConfig: &config.Config{
			ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
			Model:      config.ModelConfig{Provider: config.ProviderOpenAI, OpenAI: &config.OpenAIConfig{Model: "gpt-realtime", APIKey: "key"}},
			Session:    &config.SessionConfig{VAD: vad},
		}}
	}
	for _, testCase := range []struct {
		name string
		vad  *config.SessionVADConfig
	}{
		{name: "semantic silence duration", vad: &config.SessionVADConfig{Type: "semantic_vad", SilenceDurationMs: 500}},
		{name: "semantic invalid eagerness", vad: &config.SessionVADConfig{Type: "semantic_vad", Eagerness: "instant"}},
		{name: "server eagerness", vad: &config.SessionVADConfig{Type: "server_vad", Eagerness: "low"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := ResolveBareSessionOptions(base(testCase.vad)); err == nil {
				t.Fatal("ResolveBareSessionOptions() error = nil, want incompatible VAD policy error")
			}
		})
	}
}

func TestResolveBareSessionOptionsHonorsPersistedSessionValues(t *testing.T) {
	vadEnabled := true
	createResponse := false
	transcriptionEnabled := false
	loaded := &config.Config{
		ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
		Model: config.ModelConfig{
			Provider: config.ProviderOpenAI,
			OpenAI: &config.OpenAIConfig{
				Model:  "gpt-realtime",
				APIKey: "persisted-key",
			},
		},
		Session: &config.SessionConfig{
			Provider:     config.ProviderOpenAI,
			Model:        "gpt-realtime",
			Transport:    "WS",
			InputDevice:  "virtual:mic",
			OutputDevice: "virtual:speakers",
			VAD: &config.SessionVADConfig{
				Enabled:           &vadEnabled,
				Type:              "server_vad",
				Threshold:         0.72,
				PrefixPaddingMs:   120,
				SilenceDurationMs: 640,
				CreateResponse:    &createResponse,
			},
			InputTranscription: &config.SessionInputTranscriptionConfig{
				Enabled: &transcriptionEnabled,
				Model:   "custom-transcriber",
			},
		},
	}

	resolved, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), LoadedConfig: loaded})
	if err != nil {
		t.Fatalf("ResolveBareSessionOptions(): %v", err)
	}
	if resolved.Provider != config.ProviderOpenAI || resolved.Model != "gpt-realtime" || resolved.Transport != SessionTransportWebSocket {
		t.Fatalf("session identity = provider %q, model %q, transport %q", resolved.Provider, resolved.Model, resolved.Transport)
	}
	if resolved.APIKey != "persisted-key" {
		t.Fatalf("API key = %q, want persisted key", resolved.APIKey)
	}
	if resolved.RTCDeviceBinding.InputDevice != "virtual:mic" || resolved.RTCDeviceBinding.OutputDevice != "virtual:speakers" {
		t.Fatalf("device selectors = %#v, want persisted selectors", resolved.RTCDeviceBinding)
	}
	if resolved.TurnDetection == nil || resolved.TurnDetection.Threshold != 0.72 || resolved.TurnDetection.PrefixPaddingMs != 120 || resolved.TurnDetection.SilenceDurationMs != 640 || resolved.TurnDetection.CreateResponse == nil || *resolved.TurnDetection.CreateResponse {
		t.Fatalf("turn detection = %#v, want persisted policy", resolved.TurnDetection)
	}
	if resolved.InputAudioTranscription == nil || resolved.InputAudioTranscription.Enabled || resolved.InputAudioTranscription.Model != "custom-transcriber" {
		t.Fatalf("input transcription = %#v, want persisted policy", resolved.InputAudioTranscription)
	}
}

func TestResolveBareSessionOptionsExplicitAndAgentEnvironmentPrecedence(t *testing.T) {
	loaded := &config.Config{
		ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
		Model: config.ModelConfig{
			Provider: config.ProviderOpenAI,
			OpenAI: &config.OpenAIConfig{
				Model:  "gpt-realtime",
				APIKey: "agent-environment-key",
			},
		},
	}
	t.Setenv("OPENAI_API_KEY", "fallback-key")

	resolved, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(),
		LoadedConfig: loaded,
		APIKey:       "cli-key",
		Model:        "gpt-realtime-2.1-mini",
	})
	if err != nil {
		t.Fatalf("explicit ResolveBareSessionOptions(): %v", err)
	}
	if resolved.APIKey != "cli-key" || resolved.Model != "gpt-realtime-2.1-mini" {
		t.Fatalf("explicit precedence = key %q, model %q", resolved.APIKey, resolved.Model)
	}

	resolved, err = ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(), LoadedConfig: loaded})
	if err != nil {
		t.Fatalf("agent environment ResolveBareSessionOptions(): %v", err)
	}
	if resolved.APIKey != "agent-environment-key" {
		t.Fatalf("agent environment key = %q, want agent-environment-key", resolved.APIKey)
	}
}

func TestResolveBareSessionOptionsMissingOpenAIKeyIsActionableAndRedacted(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	configPath := filepath.Join(t.TempDir(), config.ConfigFileName)
	resolvedErr, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(),
		LoadedConfig: &config.Config{
			ConfigPath: configPath,
			Model: config.ModelConfig{
				Provider: config.ProviderOpenAI,
				OpenAI:   &config.OpenAIConfig{Model: openAIRealtimeModel},
			},
		},
	})
	if err == nil {
		t.Fatalf("ResolveBareSessionOptions() returned resolved options %#v without a key", resolvedErr)
	}
	var credentialErr *BareSessionCredentialError
	if !errors.As(err, &credentialErr) || !errors.Is(err, ErrBareSessionCredentialMissing) {
		t.Fatalf("error = %T %v, want BareSessionCredentialError", err, err)
	}
	for _, want := range []string{"OPENAI_API_KEY", configPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "sk-") || strings.Count(err.Error(), "API key") != 1 {
		t.Fatalf("credential error is not a single redacted actionable message: %q", err)
	}
}

func TestResolveBareSessionOptionsCLIDeviceSelectorsOverridePersistedValues(t *testing.T) {
	loaded := &config.Config{
		ConfigPath: filepath.Join(t.TempDir(), config.ConfigFileName),
		Model: config.ModelConfig{
			Provider: config.ProviderOpenAI,
			OpenAI:   &config.OpenAIConfig{APIKey: "key"},
		},
		Session: &config.SessionConfig{
			InputDevice:  "persisted:mic",
			OutputDevice: "persisted:speakers",
		},
	}

	resolved, err := ResolveBareSessionOptions(SessionRunOptions{ModelCatalog: testModelCatalog(),
		LoadedConfig: loaded,
		RTCDeviceBinding: RTCDeviceBindingRequest{
			InputDevice:   "cli:mic",
			OutputDevice:  "",
			InputPresent:  true,
			OutputPresent: true,
		},
	})
	if err != nil {
		t.Fatalf("ResolveBareSessionOptions(): %v", err)
	}
	if resolved.RTCDeviceBinding.InputDevice != "cli:mic" || resolved.RTCDeviceBinding.OutputDevice != "" {
		t.Fatalf("device selectors = %#v, want CLI input and explicit default output", resolved.RTCDeviceBinding)
	}
}

func TestNewLiveSessionInferencerCarriesBareAudioPolicies(t *testing.T) {
	createResponse := true
	inferencer, model, err := NewLiveSessionInferencer(SessionRunOptions{ModelCatalog: testModelCatalog(),
		Provider:  sessionProviderOpenAI,
		Model:     openAIRealtimeModel,
		APIKey:    "bare-test-key",
		ConfigDir: t.TempDir(),
		TurnDetection: &models.TurnDetectionConfig{
			Type:              "server_vad",
			Threshold:         0.68,
			PrefixPaddingMs:   160,
			SilenceDurationMs: 560,
			CreateResponse:    &createResponse,
		},
		InputAudioTranscription: &models.InputAudioTranscriptionConfig{
			Enabled: true,
			Model:   "bare-transcriber",
		},
	}, "bare instructions")
	if err != nil {
		t.Fatalf("NewLiveSessionInferencer(): %v", err)
	}
	if model != openAIRealtimeModel {
		t.Fatalf("model = %q, want %q", model, openAIRealtimeModel)
	}
	requested, ok := inferencer.(interface {
		Request() inference.SessionRequest
	})
	if !ok {
		t.Fatalf("inferencer type %T does not expose its session request", inferencer)
	}
	config := requested.Request().Config
	if config.Instructions != "bare instructions" || config.TurnDetection == nil || config.TurnDetection.Threshold != 0.68 || config.TurnDetection.SilenceDurationMs != 560 || config.TurnDetection.CreateResponse == nil || !*config.TurnDetection.CreateResponse {
		t.Fatalf("session turn policy = %#v, want resolved server VAD", config.TurnDetection)
	}
	if config.InputAudioTranscription == nil || !config.InputAudioTranscription.Enabled || config.InputAudioTranscription.Model != "bare-transcriber" {
		t.Fatalf("session transcription policy = %#v, want resolved transcription", config.InputAudioTranscription)
	}
	if config.InputAudioFormat != models.AudioFormatPCM16 || config.OutputAudioFormat != models.AudioFormatPCM16 {
		t.Fatalf("session audio formats = %q/%q, want PCM16", config.InputAudioFormat, config.OutputAudioFormat)
	}
}
