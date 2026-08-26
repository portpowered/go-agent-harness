package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestOpenAIRealtimeModels_ReturnsOrderedIndependentRegistryCopy(t *testing.T) {
	models := OpenAIRealtimeModels()
	if len(models) != 2 {
		t.Fatalf("OpenAIRealtimeModels returned %d models, want 2", len(models))
	}

	wantIDs := []string{openAIRealtimeLegacyModel, openAIRealtimeDefaultModel}
	for i, wantID := range wantIDs {
		if models[i].ID != wantID {
			t.Errorf("model %d ID = %q, want %q", i, models[i].ID, wantID)
		}
		if !models[i].SupportsAudio || !models[i].SupportsImageInput || !models[i].SupportsFunctionCalling {
			t.Errorf("model %q capabilities = %+v, want all supported", models[i].ID, models[i])
		}
	}

	models[0].ID = "mutated-by-caller"
	models[0].SupportsAudio = false
	got, ok := LookupOpenAIRealtimeModel(openAIRealtimeLegacyModel)
	if !ok {
		t.Fatal("legacy realtime model disappeared after caller mutation")
	}
	if got.ID != openAIRealtimeLegacyModel || !got.SupportsAudio {
		t.Fatalf("registry was mutated through returned slice: %+v", got)
	}
}

func TestLookupOpenAIRealtimeModel_MiniReportsCapabilities(t *testing.T) {
	model, ok := LookupOpenAIRealtimeModel(openAIRealtimeDefaultModel)
	if !ok {
		t.Fatal("mini realtime model was not registered")
	}
	if !model.SupportsAudio || !model.SupportsImageInput || !model.SupportsFunctionCalling {
		t.Fatalf("mini realtime model capabilities = %+v, want all supported", model)
	}

	if _, ok := LookupOpenAIRealtimeModel(strings.ToUpper(openAIRealtimeDefaultModel)); ok {
		t.Fatal("wrong-case realtime model should not be accepted")
	}
	if _, ok := LookupOpenAIRealtimeModel("not-a-model"); ok {
		t.Fatal("unknown realtime model should not be accepted")
	}
}

func TestNewOpenAIRealtimeSessionInferencer_UnsupportedModelsRejectBeforeDial(t *testing.T) {
	tests := []struct {
		name  string
		model string
	}{
		{name: "unknown", model: "not-a-model"},
		{name: "chat-only", model: "gpt-4o"},
		{name: "empty", model: ""},
		{name: "whitespace", model: "   "},
		{name: "wrong-case", model: "GPT-REALTIME-2.1-MINI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialer := &recordingOpenAIRealtimeDialer{}
			_, err := NewOpenAIRealtimeSessionInferencerWithOptions(
				configOpenAIRealtimeTestConfig(tt.model),
				oaiprovider.WithWebSocketDialer(dialer),
			)
			if err == nil {
				t.Fatal("expected unsupported model error")
			}
			if !errors.Is(err, ErrUnsupportedRealtimeModel) {
				t.Fatalf("error = %v, want ErrUnsupportedRealtimeModel", err)
			}
			var unsupported *UnsupportedRealtimeModelError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %v, want UnsupportedRealtimeModelError", err)
			}
			if unsupported.Model != tt.model {
				t.Fatalf("rejected model = %q, want %q", unsupported.Model, tt.model)
			}
			for _, supported := range []string{openAIRealtimeLegacyModel, openAIRealtimeDefaultModel} {
				if !strings.Contains(err.Error(), supported) {
					t.Fatalf("error %q does not list supported model %q", err, supported)
				}
			}
			if dialer.calls != 0 {
				t.Fatalf("unsupported model attempted %d dials", dialer.calls)
			}
		})
	}
}

func TestNewOpenAIRealtimeSessionInferencer_SupportedModelsReachDialer(t *testing.T) {
	for _, model := range []string{openAIRealtimeLegacyModel, openAIRealtimeDefaultModel} {
		t.Run(model, func(t *testing.T) {
			dialer := &recordingOpenAIRealtimeDialer{dialErr: errors.New("dial stopped by test")}
			inferencer, err := NewOpenAIRealtimeSessionInferencerWithOptions(
				configOpenAIRealtimeTestConfig(model),
				oaiprovider.WithRealtimeBaseURL("wss://test.openai.example/realtime"),
				oaiprovider.WithWebSocketDialer(dialer),
			)
			if err != nil {
				t.Fatalf("NewOpenAIRealtimeSessionInferencerWithOptions: %v", err)
			}

			_, err = inferencer.ConnectSession(context.Background())
			if err == nil || !strings.Contains(err.Error(), "dial stopped by test") {
				t.Fatalf("ConnectSession error = %v, want injected dial error", err)
			}
			if dialer.calls != 1 {
				t.Fatalf("dial calls = %d, want 1", dialer.calls)
			}
			if !strings.Contains(dialer.url, "model="+model) {
				t.Fatalf("dial URL = %q, want selected model %q", dialer.url, model)
			}
		})
	}
}

func TestNewLiveSessionInferencerBuildsAudioSessionRequest(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		apiKey   string
		baseURL  string
	}{
		{name: "openai", provider: config.ProviderOpenAI, model: openAIRealtimeDefaultModel, apiKey: "sk-live-test", baseURL: "ws://openai.test/realtime"},
		{name: "grok", provider: config.ProviderGrok, model: "grok-session-model", apiKey: "xai-live-test", baseURL: "ws://grok.test/realtime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferencer, model, err := NewLiveSessionInferencer(SessionRunOptions{
				Provider:  tt.provider,
				Model:     tt.model,
				APIKey:    tt.apiKey,
				BaseURL:   tt.baseURL,
				ConfigDir: t.TempDir(),
			}, "respond with the device probe phrase")
			if err != nil {
				t.Fatalf("NewLiveSessionInferencer: %v", err)
			}
			if inferencer == nil || model != tt.model {
				t.Fatalf("inferencer/model = (%T, %q), want non-nil inferencer and %q", inferencer, model, tt.model)
			}
			requested, ok := inferencer.(interface {
				Request() inference.SessionRequest
			})
			if !ok {
				t.Fatalf("inferencer type %T does not expose its session request", inferencer)
			}
			config := requested.Request().Config
			if config.Model != tt.model || config.Instructions != "respond with the device probe phrase" {
				t.Fatalf("session request identity = (%q, %q), want model/instructions", config.Model, config.Instructions)
			}
			if len(config.Modalities) != 2 || config.Modalities[0] != models.SessionModalityText || config.Modalities[1] != models.SessionModalityAudio {
				t.Fatalf("session modalities = %#v, want text+audio", config.Modalities)
			}
			if config.InputAudioFormat != models.AudioFormatPCM16 || config.OutputAudioFormat != models.AudioFormatPCM16 ||
				config.InputAudioSampleRate != models.SampleRate24000 || config.OutputAudioSampleRate != models.SampleRate24000 {
				t.Fatalf("session audio contract = %#v, want PCM16 at 24 kHz in both directions", config)
			}
		})
	}
}

func TestRunSession_OpenAIRealtimeDefaultAndOverridesReachDialer(t *testing.T) {
	for _, model := range []string{"", openAIRealtimeLegacyModel, openAIRealtimeDefaultModel} {
		name := model
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			configDir := t.TempDir()
			writeSessionConfigFile(t, configDir, `
model:
  provider: openai
  openai:
    api_key: sk-test-key
`)
			dialer := &recordingGrokRealtimeDialer{dialErr: errors.New("dial stopped by test")}
			_, _ = runOpenAIRealtimeWithDialer(t, configDir, model, dialer)
			if dialer.calls != 1 {
				t.Fatalf("dial calls = %d, want 1", dialer.calls)
			}
			wantModel := model
			if wantModel == "" {
				wantModel = openAIRealtimeDefaultModel
			}
			if !strings.Contains(dialer.url, "model="+wantModel) {
				t.Fatalf("dial URL = %q, want selected model %q", dialer.url, wantModel)
			}
		})
	}
}

func TestResolveOpenAIRealtimeSessionConfig_DefaultsWhenProviderConfigHasNoModel(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: openai
`)

	got, err := resolveOpenAIRealtimeSessionConfig(SessionRunOptions{
		Provider:  config.ProviderOpenAI,
		APIKey:    "sk-test-key",
		ConfigDir: configDir,
	})
	if err != nil {
		t.Fatalf("resolveOpenAIRealtimeSessionConfig: %v", err)
	}
	if got.Model != openAIRealtimeDefaultModel {
		t.Fatalf("default realtime model = %q, want %q", got.Model, openAIRealtimeDefaultModel)
	}
}

func TestRunSession_OpenAIRealtimeExplicitEmptyModelRejectsBeforeDial(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: openai
  openai:
    api_key: sk-test-key
`)
	dialer := &recordingGrokRealtimeDialer{dialErr: errors.New("dial should not be reached")}

	err := RunSession(context.Background(), &strings.Builder{}, SessionRunOptions{
		RecordPath:      filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:        config.ProviderOpenAI,
		ModelProvided:   true,
		ConfigDir:       configDir,
		WebSocketDialer: dialer,
	})
	if err == nil {
		t.Fatal("expected explicit empty model rejection")
	}
	if !errors.Is(err, ErrUnsupportedRealtimeModel) {
		t.Fatalf("error = %v, want ErrUnsupportedRealtimeModel", err)
	}
	var unsupported *UnsupportedRealtimeModelError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedRealtimeModelError", err)
	}
	if unsupported.Model != "" {
		t.Fatalf("rejected model = %q, want empty model", unsupported.Model)
	}
	if dialer.calls != 0 {
		t.Fatalf("explicit empty model attempted %d dials", dialer.calls)
	}
}

func TestResolveOpenAIRealtimeSessionConfig_WhitespaceOverrideIsTypedRejection(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: openai
  openai:
    api_key: sk-test-key
`)

	_, err := resolveOpenAIRealtimeSessionConfig(SessionRunOptions{
		Provider:  config.ProviderOpenAI,
		Model:     "   ",
		ConfigDir: configDir,
	})
	if err == nil {
		t.Fatal("expected whitespace model rejection")
	}
	if !errors.Is(err, ErrUnsupportedRealtimeModel) {
		t.Fatalf("error = %v, want ErrUnsupportedRealtimeModel", err)
	}
}

func runOpenAIRealtimeWithDialer(t *testing.T, configDir, model string, dialer transport.Dialer) (string, error) {
	t.Helper()
	var out strings.Builder
	err := RunSession(context.Background(), &out, SessionRunOptions{
		RecordPath:      filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:        config.ProviderOpenAI,
		Model:           model,
		ConfigDir:       configDir,
		WebSocketDialer: dialer,
	})
	return out.String(), err
}

func configOpenAIRealtimeTestConfig(model string) config.OpenAIConfig {
	return config.OpenAIConfig{APIKey: "sk-test-key", Model: model}
}

type recordingOpenAIRealtimeDialer struct {
	calls   int
	url     string
	dialErr error
}

var _ oaiprovider.WebSocketDialer = (*recordingOpenAIRealtimeDialer)(nil)

func (d *recordingOpenAIRealtimeDialer) Dial(url string, _ map[string]string) (oaiprovider.WebSocketConn, error) {
	d.calls++
	d.url = url
	return nil, d.dialErr
}

type recordingGrokRealtimeDialer struct {
	calls   int
	url     string
	dialErr error
}

var _ transport.Dialer = (*recordingGrokRealtimeDialer)(nil)

func (d *recordingGrokRealtimeDialer) Dial(url string, _ map[string]string) (transport.Conn, error) {
	d.calls++
	d.url = url
	return nil, d.dialErr
}
