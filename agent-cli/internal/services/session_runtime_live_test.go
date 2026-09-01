package services

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestPlanSessionRuntime_BrowserToolsUsesUnrecordedLiveRuntime(t *testing.T) {
	dialer := &stubRuntimeDialer{id: "browser-live"}
	var gotConfig config.GrokConfig
	var gotDefinitions []messages.ToolDefinition
	factory := sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return dialer },
		newGrokSessionWithTools: func(cfg config.GrokConfig, gotDialer transport.Dialer, definitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
			if gotDialer != dialer {
				t.Fatalf("browser live inferencer dialer = %v, want factory-owned dialer", gotDialer)
			}
			gotConfig = cfg
			gotDefinitions = append([]messages.ToolDefinition(nil), definitions...)
			return &scriptedSessionInferencer{}, nil
		},
	}
	loaded := &config.Config{
		Model: config.ModelConfig{
			Provider: config.ProviderGrok,
			Grok:     &config.GrokConfig{Model: "grok-browser-test", APIKey: "test-key"},
		},
	}
	definitions := []messages.ToolDefinition{{Name: "browser_test"}}
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		Provider:                config.ProviderGrok,
		BrowserToolsEnabled:     true,
		BrowserToolsInteractive: true,
		LoadedConfig:            loaded,
		ToolDefinitions:         definitions,
	}, factory)
	if err != nil {
		t.Fatalf("plan browser live runtime: %v", err)
	}
	if plan.mode != sessionRuntimeModeInjectedLive || plan.provider != config.ProviderGrok || plan.model != "grok-browser-test" {
		t.Fatalf("browser live plan identity = mode:%q provider:%q model:%q", plan.mode, plan.provider, plan.model)
	}
	if plan.capturePath != "" || plan.flushCapture != nil || plan.finalize != nil {
		t.Fatalf("browser live plan unexpectedly owns capture lifecycle: %+v", plan)
	}
	if plan.inferencer == nil || plan.loop.CloseAfterOpen || !plan.loop.WaitForClose || !plan.loop.BrowserToolsInteractive || plan.loop.AdvertiseToolDefinitions {
		t.Fatalf("browser live plan lifecycle = %+v", plan.loop)
	}
	if gotConfig.Model != "grok-browser-test" || gotConfig.APIKey != "test-key" || len(gotDefinitions) != 1 || gotDefinitions[0].Name != "browser_test" {
		t.Fatalf("browser live provider inputs = %+v/%v", gotConfig, gotDefinitions)
	}
}

func TestPlanSessionRuntime_BrowserToolsDefaultProviderFallsBackToOpenAI(t *testing.T) {
	dialer := &stubRuntimeDialer{id: "browser-openai-default"}
	var gotConfig config.OpenAIConfig
	factory := sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return dialer },
		newOpenAISessionWithTools: func(cfg config.OpenAIConfig, _ string, gotDialer transport.Dialer, _ []messages.ToolDefinition, _ models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			if gotDialer != dialer {
				t.Fatalf("browser default inferencer dialer = %v, want factory-owned dialer", gotDialer)
			}
			gotConfig = cfg
			return &scriptedSessionInferencer{}, nil
		},
	}
	loaded := &config.Config{
		Model: config.ModelConfig{
			Provider: config.ProviderOpenRouter,
			OpenRouter: &config.OpenAIConfig{
				Model: config.DefaultModelModel,
			},
		},
	}

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		BrowserToolsEnabled: true,
		LoadedConfig:        loaded,
		APIKey:              "openai-default-key",
		Prompt:              "use the browser",
		ToolDefinitions:     []messages.ToolDefinition{{Name: "browser_test"}},
	}, factory)
	if err != nil {
		t.Fatalf("plan browser default runtime: %v", err)
	}
	if plan.provider != config.ProviderOpenAI || plan.model != openAIRealtimeModel {
		t.Fatalf("browser default plan identity = provider:%q model:%q, want openai/%q", plan.provider, plan.model, openAIRealtimeModel)
	}
	if gotConfig.Model != openAIRealtimeModel || gotConfig.APIKey != "openai-default-key" {
		t.Fatalf("browser default provider config = %#v, want realtime OpenAI config", gotConfig)
	}
}

func TestPlanSessionRuntime_BrowserToolsRejectsUnsupportedProvider(t *testing.T) {
	_, err := planSessionRuntimeWithFactory(SessionRunOptions{
		Provider:            "unsupported-provider",
		BrowserToolsEnabled: true,
	}, sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return &stubRuntimeDialer{id: "unused"} },
	})
	want := unsupportedRealtimeSessionProviderError("unsupported-provider").Error()
	if err == nil || err.Error() != want {
		t.Fatalf("unsupported browser provider error = %v", err)
	}
}

func TestPlanSessionRuntime_UnsupportedProviderDiagnosticsAreShared(t *testing.T) {
	const provider = "unsupported-provider"
	tests := []struct {
		name string
		opts SessionRunOptions
	}{
		{
			name: "browser tools",
			opts: SessionRunOptions{Provider: provider, BrowserToolsEnabled: true},
		},
		{
			name: "recording",
			opts: SessionRunOptions{Provider: provider, RecordPath: filepath.Join(t.TempDir(), "capture.json")},
		},
	}

	want := unsupportedRealtimeSessionProviderError(provider).Error()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := planSessionRuntimeWithFactory(testCase.opts, sessionRuntimeFactory{})
			if err == nil {
				t.Fatal("unsupported provider unexpectedly planned")
			}
			if err.Error() != want {
				t.Fatalf("unsupported provider error = %q, want %q", err, want)
			}
		})
	}
}

func TestPlanSessionRuntime_RecordDefaultProviderFallsBackToOpenAI(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "default-openai.session.json")
	liveDialer := &stubRuntimeDialer{id: "record-openai-default-live"}
	recordingDialer := &browserRecordingDialer{provider: config.ProviderOpenAI, model: openAIRealtimeModel}
	var gotConfig config.OpenAIConfig
	var gotDialer transport.Dialer
	factory := sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return liveDialer },
		newRecordingDialer: func(inner transport.Dialer, provider, model string) sessionRecordingDialer {
			if provider != config.ProviderOpenAI || model != openAIRealtimeModel {
				t.Fatalf("record default metadata = (%q, %q), want (%q, %q)", provider, model, config.ProviderOpenAI, openAIRealtimeModel)
			}
			recordingDialer.inner = inner
			return recordingDialer
		},
		newOpenAISessionWithTools: func(cfg config.OpenAIConfig, _ string, dialer transport.Dialer, _ []messages.ToolDefinition, _ models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			gotConfig = cfg
			gotDialer = dialer
			return &scriptedSessionInferencer{}, nil
		},
	}
	loaded := &config.Config{
		Model: config.ModelConfig{
			Provider: config.ProviderOpenRouter,
			OpenRouter: &config.OpenAIConfig{
				Model: config.DefaultModelModel,
			},
		},
	}

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		RecordPath:   recordPath,
		LoadedConfig: loaded,
		APIKey:       "openai-default-key",
		Prompt:       "answer after the audio turn",
		AudioInputs:  []ScheduledAudioInput{{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true}},
	}, factory)
	if err != nil {
		t.Fatalf("plan record default runtime: %v", err)
	}
	if plan.mode != sessionRuntimeModeRecordOpenAI || plan.provider != config.ProviderOpenAI || plan.model != openAIRealtimeModel {
		t.Fatalf("record default plan identity = mode:%q provider:%q model:%q", plan.mode, plan.provider, plan.model)
	}
	if !plan.loop.RequireSessionUpdated {
		t.Fatal("record default audio plan did not retain OpenAI SESSION.UPDATED requirement")
	}
	if gotConfig.Model != openAIRealtimeModel || gotConfig.APIKey != "openai-default-key" {
		t.Fatalf("record default provider config = %#v, want realtime OpenAI config", gotConfig)
	}
	if gotDialer != recordingDialer || recordingDialer.inner != liveDialer {
		t.Fatalf("record default dialer chain = provider:%T inner:%T, want recording over live", gotDialer, recordingDialer.inner)
	}
}

func TestPlanSessionRuntime_BrowserToolsWithRecordingPreservesCaptureLifecycle(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		provider string
		model    string
		apiKey   string
		mode     sessionRuntimeMode
	}{
		{name: "grok", provider: config.ProviderGrok, model: "grok-browser-record-test", apiKey: "test-grok-key", mode: sessionRuntimeModeRecordGrok},
		{name: "openai", provider: config.ProviderOpenAI, model: "gpt-realtime", apiKey: "test-openai-key", mode: sessionRuntimeModeRecordOpenAI},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recordPath := filepath.Join(t.TempDir(), "browser-session.json")
			liveDialer := &stubRuntimeDialer{id: testCase.name + "-live"}
			recordingDialer := &browserRecordingDialer{provider: testCase.provider, model: testCase.model}
			var gotDialer transport.Dialer
			factory := sessionRuntimeFactory{
				newDefaultLiveDialer: func() transport.Dialer { return liveDialer },
				newRecordingDialer: func(inner transport.Dialer, provider, model string) sessionRecordingDialer {
					recordingDialer.inner = inner
					recordingDialer.provider = provider
					recordingDialer.model = model
					return recordingDialer
				},
			}
			loaded := &config.Config{Model: config.ModelConfig{Provider: testCase.provider}}
			switch testCase.provider {
			case config.ProviderGrok:
				loaded.Model.Grok = &config.GrokConfig{Model: testCase.model, APIKey: testCase.apiKey}
				factory.newGrokSessionWithTools = func(_ config.GrokConfig, dialer transport.Dialer, _ []messages.ToolDefinition) (messages.SessionInferencer, error) {
					gotDialer = dialer
					return &scriptedSessionInferencer{}, nil
				}
			case config.ProviderOpenAI:
				loaded.Model.OpenAI = &config.OpenAIConfig{Model: testCase.model, APIKey: testCase.apiKey}
				factory.newOpenAISessionWithTools = func(_ config.OpenAIConfig, _ string, dialer transport.Dialer, _ []messages.ToolDefinition, _ models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
					gotDialer = dialer
					return &scriptedSessionInferencer{}, nil
				}
			}

			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				RecordPath:          recordPath,
				Provider:            testCase.provider,
				BrowserToolsEnabled: true,
				LoadedConfig:        loaded,
				ToolDefinitions:     []messages.ToolDefinition{{Name: "browser_test"}},
			}, factory)
			if err != nil {
				t.Fatalf("plan browser recording runtime: %v", err)
			}
			if plan.mode != testCase.mode {
				t.Fatalf("browser recording plan mode = %q, want %q", plan.mode, testCase.mode)
			}
			if plan.capturePath != recordPath || plan.flushCapture == nil || plan.finalize == nil {
				t.Fatalf("browser recording plan capture lifecycle = path:%q flush:%t finalize:%t", plan.capturePath, plan.flushCapture != nil, plan.finalize != nil)
			}
			if gotDialer != recordingDialer {
				t.Fatalf("%s provider received %T, want recording dialer", testCase.provider, gotDialer)
			}
			if recordingDialer.inner != liveDialer {
				t.Fatalf("%s recording dialer wrapped %T, want caller live dialer", testCase.provider, recordingDialer.inner)
			}

			var out bytes.Buffer
			if err := plan.run(context.Background(), &out); err != nil {
				t.Fatalf("run browser recording plan: %v", err)
			}
			if recordingDialer.flushCalls != 1 || filepath.Dir(recordingDialer.path) != filepath.Dir(recordPath) || !strings.HasPrefix(filepath.Base(recordingDialer.path), "."+filepath.Base(recordPath)+".tmp-") {
				t.Fatalf("%s recording flush = calls:%d path:%q, want one private same-directory temporary artifact for %q", testCase.provider, recordingDialer.flushCalls, recordingDialer.path, recordPath)
			}
			capture, err := gwtesting.LoadSessionCapture(recordPath)
			if err != nil {
				t.Fatalf("load emitted %s browser capture: %v", testCase.provider, err)
			}
			if capture.Provider.Name != testCase.provider || capture.Provider.Model != testCase.model {
				t.Fatalf("emitted %s capture provider = %#v", testCase.provider, capture.Provider)
			}
			if !strings.Contains(out.String(), "Wrote session capture to "+recordPath) {
				t.Fatalf("%s recording finalizer output = %q", testCase.provider, out.String())
			}
		})
	}
}

type browserRecordingDialer struct {
	inner      transport.Dialer
	provider   string
	model      string
	path       string
	flushCalls int
}

func (d *browserRecordingDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	return d.inner.Dial(endpoint, headers)
}

func (d *browserRecordingDialer) FlushToFile(path string) error {
	d.flushCalls++
	d.path = path
	data, err := json.Marshal(gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: d.provider, Model: d.model},
		Records:  []gwtesting.CapturedSessionEvent{},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
