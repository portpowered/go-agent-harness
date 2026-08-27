package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestPlanSessionRuntime_OpenAIRecordOwnsConfigAndDialerSelection(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: openai
  openai:
    model: gpt-realtime
    api_key: sk-config-key
`)

	defaultDialer := &stubRuntimeDialer{id: "default-live"}
	recordingDialer := &stubRecordingDialer{stubRuntimeDialer: stubRuntimeDialer{id: "recording-openai"}}
	var gotInner transport.Dialer
	var gotProvider string
	var gotModel string
	var gotVoice string
	var gotCfg config.OpenAIConfig
	var gotDialer transport.Dialer

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		RecordPath: filepath.Join(t.TempDir(), "openai.session.json"),
		Provider:   config.ProviderOpenAI,
		Model:      "gpt-realtime",
		APIKey:     "sk-override-key",
		ConfigDir:  configDir,
		Voice:      "marin",
	}, sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return defaultDialer },
		newRecordingDialer: func(inner transport.Dialer, providerName string, model string) sessionRecordingDialer {
			gotInner = inner
			gotProvider = providerName
			gotModel = model
			return recordingDialer
		},
		newOpenAISessionInf: func(cfg config.OpenAIConfig, voice string, dialer transport.Dialer) (messages.SessionInferencer, error) {
			gotCfg = cfg
			gotVoice = voice
			gotDialer = dialer
			return &scriptedSessionInferencer{}, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}

	if plan.mode != sessionRuntimeModeRecordOpenAI {
		t.Fatalf("plan.mode = %q, want %q", plan.mode, sessionRuntimeModeRecordOpenAI)
	}
	if gotInner != defaultDialer {
		t.Fatal("OpenAI record runtime did not use the factory-owned default live dialer")
	}
	if gotProvider != config.ProviderOpenAI || gotModel != "gpt-realtime" {
		t.Fatalf("recording dialer metadata = (%q, %q), want (%q, %q)", gotProvider, gotModel, config.ProviderOpenAI, "gpt-realtime")
	}
	if gotDialer != recordingDialer {
		t.Fatal("OpenAI record inferencer did not receive the owned recording dialer")
	}
	if gotCfg.APIKey != "sk-override-key" || gotCfg.Model != "gpt-realtime" {
		t.Fatalf("OpenAI config overrides were not resolved before runtime planning: %#v", gotCfg)
	}
	if gotVoice != "marin" {
		t.Fatalf("OpenAI runtime voice = %q, want marin", gotVoice)
	}
}

func TestPlanSessionRuntime_ScheduledAudioUsesPersistentLiveLifecycle(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		provider  string
		model     string
		apiKey    string
		plan      func(SessionRunOptions, sessionRuntimeFactory) (sessionRuntimePlan, error)
		configure func(*sessionRuntimeFactory)
	}{
		{
			name:     "openai",
			provider: config.ProviderOpenAI,
			model:    "gpt-realtime",
			apiKey:   "sk-scheduled-test-key",
			plan:     planOpenAIRecordRuntime,
			configure: func(factory *sessionRuntimeFactory) {
				factory.newOpenAISessionInf = func(config.OpenAIConfig, transport.Dialer) (messages.SessionInferencer, error) {
					return &scriptedSessionInferencer{}, nil
				}
			},
		},
		{
			name:     "grok",
			provider: config.ProviderGrok,
			model:    "grok-realtime",
			apiKey:   "xai-scheduled-test-key",
			plan:     planGrokRecordRuntime,
			configure: func(factory *sessionRuntimeFactory) {
				factory.newGrokSessionInferencer = func(config.GrokConfig, transport.Dialer) (messages.SessionInferencer, error) {
					return &scriptedSessionInferencer{}, nil
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recordPath := filepath.Join(t.TempDir(), "capture.json")
			factory := sessionRuntimeFactory{
				newDefaultLiveDialer: func() transport.Dialer {
					return &stubRuntimeDialer{id: "scheduled-live"}
				},
				newRecordingDialer: func(transport.Dialer, string, string) sessionRecordingDialer {
					return &stubRecordingDialer{stubRuntimeDialer: stubRuntimeDialer{id: "scheduled-recording"}}
				},
			}
			testCase.configure(&factory)

			plan, err := testCase.plan(SessionRunOptions{
				RecordPath: recordPath,
				Provider:   testCase.provider,
				Model:      testCase.model,
				APIKey:     testCase.apiKey,
				ConfigDir:  t.TempDir(),
				AudioInputs: []ScheduledAudioInput{
					{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true},
					{AfterCompletedTurns: 1, PCM: []byte{3, 4}, EndOfTurn: true},
				},
			}, factory)
			if err != nil {
				t.Fatalf("plan scheduled %s runtime: %v", testCase.provider, err)
			}
			if plan.loop.CloseAfterOpen {
				t.Fatal("scheduled live session still closes immediately after SESSION.OPEN")
			}
			if !plan.loop.WaitForClose {
				t.Fatal("scheduled live session must wait for its explicit terminal close")
			}
			if !plan.loop.CloseAfterScheduledAudio {
				t.Fatal("scheduled live session must close only after the scheduled responses")
			}
			if plan.capturePath != recordPath {
				t.Fatalf("capture path = %q, want %q", plan.capturePath, recordPath)
			}
		})
	}
}

func TestPlanSessionRuntime_GrokRecordPreservesCallerOwnedDialer(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: grok
  grok:
    model: grok-config-model
    api_key: xai-config-key
`)

	callerDialer := &stubRuntimeDialer{id: "caller-live"}
	recordingDialer := &stubRecordingDialer{stubRuntimeDialer: stubRuntimeDialer{id: "recording-grok"}}
	var defaultDialerCalled bool
	var gotInner transport.Dialer
	var gotCfg config.GrokConfig
	var gotDialer transport.Dialer

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		RecordPath:      filepath.Join(t.TempDir(), "grok.session.json"),
		Provider:        config.ProviderGrok,
		Model:           "grok-override-model",
		APIKey:          "xai-override-key",
		ConfigDir:       configDir,
		WebSocketDialer: callerDialer,
	}, sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer {
			defaultDialerCalled = true
			return &stubRuntimeDialer{id: "unexpected-default"}
		},
		newRecordingDialer: func(inner transport.Dialer, _ string, _ string) sessionRecordingDialer {
			gotInner = inner
			return recordingDialer
		},
		newGrokSessionInferencer: func(cfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
			gotCfg = cfg
			gotDialer = dialer
			return &scriptedSessionInferencer{}, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}

	if plan.mode != sessionRuntimeModeRecordGrok {
		t.Fatalf("plan.mode = %q, want %q", plan.mode, sessionRuntimeModeRecordGrok)
	}
	if defaultDialerCalled {
		t.Fatal("Grok record runtime should keep the caller-owned live dialer")
	}
	if gotInner != callerDialer {
		t.Fatal("Grok record runtime did not pass the caller-owned dialer into the recording seam")
	}
	if gotDialer != recordingDialer {
		t.Fatal("Grok session inferencer did not receive the owned recording dialer")
	}
	if gotCfg.APIKey != "xai-override-key" || gotCfg.Model != "grok-override-model" {
		t.Fatalf("Grok config overrides were not resolved before runtime planning: %#v", gotCfg)
	}
}

func TestPlanSessionRuntime_RecordRejectsMissingOwnedDialer(t *testing.T) {
	configDir := t.TempDir()
	writeSessionConfigFile(t, configDir, `
model:
  provider: grok
  grok:
    model: grok-config-model
    api_key: xai-config-key
`)

	_, err := planSessionRuntimeWithFactory(SessionRunOptions{
		RecordPath: filepath.Join(t.TempDir(), "grok.session.json"),
		Provider:   config.ProviderGrok,
		ConfigDir:  configDir,
	}, sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer { return nil },
	})
	if err == nil {
		t.Fatal("expected record runtime planning to reject a missing owned dialer")
	}
	if !strings.Contains(err.Error(), "requires an injected websocket dialer") {
		t.Fatalf("expected missing dialer contract error, got: %v", err)
	}
}

func TestPlanSessionRuntime_OpenAIReplayRoutesThroughOpenAIRuntimeSeam(t *testing.T) {
	var openAICalled bool
	var grokCalled bool
	var gotVoice string
	replayDialer := &stubReplayDialer{
		stubRuntimeDialer: stubRuntimeDialer{id: "openai-replay"},
		model:             "gpt-realtime",
		done:              make(chan struct{}),
	}

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		ReplayPath: filepath.Join("..", "..", "test", "integration", "testdata", "openai_realtime_text.session.json"),
		Prompt:     "hello realtime",
		Voice:      "cedar",
	}, sessionRuntimeFactory{
		newReplayDialer: func(path string) (sessionReplayDialer, error) {
			if !strings.Contains(path, "openai_realtime_text.session.json") {
				t.Fatalf("unexpected replay path: %s", path)
			}
			return replayDialer, nil
		},
		newOpenAISessionInf: func(cfg config.OpenAIConfig, voice string, dialer transport.Dialer) (messages.SessionInferencer, error) {
			openAICalled = true
			gotVoice = voice
			if cfg.Model != "gpt-realtime" {
				t.Fatalf("OpenAI replay model = %q, want gpt-realtime", cfg.Model)
			}
			if dialer != replayDialer {
				t.Fatal("OpenAI replay runtime did not inject the replay dialer into the provider seam")
			}
			return &scriptedSessionInferencer{}, nil
		},
		newGrokSessionInferencer: func(config.GrokConfig, transport.Dialer) (messages.SessionInferencer, error) {
			grokCalled = true
			return &scriptedSessionInferencer{}, nil
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}

	if plan.mode != sessionRuntimeModeReplayOpenAI {
		t.Fatalf("plan.mode = %q, want %q", plan.mode, sessionRuntimeModeReplayOpenAI)
	}
	if !openAICalled {
		t.Fatal("OpenAI websocket replay capture did not route through the OpenAI runtime seam")
	}
	if grokCalled {
		t.Fatal("OpenAI websocket replay capture should not use the Grok runtime seam")
	}
	if gotVoice != "cedar" {
		t.Fatalf("OpenAI replay voice = %q, want cedar", gotVoice)
	}
}

func TestPlanSessionRuntime_OpenAIReplayUsesToolDefinitionsOnlyWhenCaptureAdvertisesTools(t *testing.T) {
	definition := messages.ToolDefinition{
		Name:        "exec",
		Description: "execute a command",
		Parameters: []messages.ToolParameter{{
			Name:     "command",
			Type:     "string",
			Required: true,
		}},
	}

	cases := []struct {
		name          string
		sessionUpdate string
		wantProvider  int
	}{
		{
			name:          "historical capture without tools",
			sessionUpdate: `{"type":"session.update","session":{"model":"gpt-realtime"}}`,
			wantProvider:  0,
		},
		{
			name:          "strict capture with tools",
			sessionUpdate: `{"type":"session.update","session":{"model":"gpt-realtime","tools":[]}}`,
			wantProvider:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "openai-replay.session.json")
			writeOpenAIReplayCapture(t, path, tc.sessionUpdate)

			replayDialer := &stubReplayDialer{
				stubRuntimeDialer: stubRuntimeDialer{id: "openai-replay"},
				model:             "gpt-realtime",
				done:              make(chan struct{}),
			}
			var gotProviderDefinitions []messages.ToolDefinition
			plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
				ReplayPath:      path,
				ToolDefinitions: []messages.ToolDefinition{definition},
			}, sessionRuntimeFactory{
				newReplayDialer: func(string) (sessionReplayDialer, error) {
					return replayDialer, nil
				},
				newOpenAISessionWithTools: func(_ config.OpenAIConfig, _ string, _ transport.Dialer, definitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
					gotProviderDefinitions = definitions
					return &scriptedSessionInferencer{}, nil
				},
			})
			if err != nil {
				t.Fatalf("planSessionRuntimeWithFactory: %v", err)
			}
			if len(gotProviderDefinitions) != tc.wantProvider {
				t.Fatalf("provider tool definitions = %#v, want %d definitions", gotProviderDefinitions, tc.wantProvider)
			}
			if len(plan.loop.ToolDefinitions) != 1 {
				t.Fatalf("replay loop tool definitions = %#v, want selected definition retained for loop execution", plan.loop.ToolDefinitions)
			}
		})
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestWriteSessionReplayMessage_PrintsSessionTerminalFields(t *testing.T) {
	var out bytes.Buffer

	err := writeSessionReplayMessage(&out, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"session-1",
			"provider_closed",
			string(messages.TerminalReasonProviderClose),
			messages.TerminalReasonProviderClose,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNotApplicable,
		),
	})
	if err != nil {
		t.Fatalf("writeSessionReplayMessage: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "[session closed: provider_closed]") {
		t.Fatalf("legacy close line missing from output:\n%s", got)
	}
	for _, want := range []string{
		"classification=provider_close",
		"terminal_reason=provider_close",
		"terminal_provenance=provider",
		"output_state=not_applicable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("terminal output missing %q:\n%s", want, got)
		}
	}
}

func TestWriteSessionReplayMessage_PrintsTranscriptDelta(t *testing.T) {
	var out bytes.Buffer

	err := writeSessionReplayMessage(&out, messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptDelta,
		Value: messages.NewTranscriptDeltaValue("spoken image description"),
	})
	if err != nil {
		t.Fatalf("writeSessionReplayMessage: %v", err)
	}
	if got := out.String(); got != "spoken image description" {
		t.Fatalf("transcript output = %q, want %q", got, "spoken image description")
	}
}

func TestWriteSessionReplayMessage_ReturnsSessionErrorTerminalFields(t *testing.T) {
	err := writeSessionReplayMessage(io.Discard, messages.StreamMessage{
		Type: messages.StreamTypeError,
		Value: messages.NewErrorValueWithTerminal(
			"provider rejected request",
			"provider_rejected",
			messages.TerminalReasonTerminalFailure,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNone,
		),
	})
	if err == nil {
		t.Fatal("expected session error")
	}

	got := err.Error()
	for _, want := range []string{
		"session error: provider rejected request",
		"classification=provider_rejected",
		"terminal_reason=terminal_failure",
		"terminal_provenance=provider",
		"output_state=none",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("session error missing %q: %v", want, err)
		}
	}
}

type stubInferencer struct{}

func (stubInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, "ok"),
	}, nil
}

func (stubInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

func TestNewGrokSessionInferencer_BuildsSessionCapableProviderPath(t *testing.T) {
	inf, err := NewGrokSessionInferencer(config.GrokConfig{
		Model:  "grok-session-model",
		APIKey: "xai-test-key",
	})
	if err != nil {
		t.Fatalf("NewGrokSessionInferencer: %v", err)
	}
	if inf == nil {
		t.Fatal("NewGrokSessionInferencer returned nil")
	}

	var _ = messages.SessionInferencer(inf)
}

func TestNewOpenAIRealtimeSessionInferencer_BuildsSessionCapableProviderPath(t *testing.T) {
	inf, err := NewOpenAIRealtimeSessionInferencer(config.OpenAIConfig{
		Model:  "gpt-realtime",
		APIKey: "sk-test-key",
	})
	if err != nil {
		t.Fatalf("NewOpenAIRealtimeSessionInferencer: %v", err)
	}
	if inf == nil {
		t.Fatal("NewOpenAIRealtimeSessionInferencer returned nil")
	}

	var _ = messages.SessionInferencer(inf)
}

func TestOpenAIRealtimeURL_AddsModelQuery(t *testing.T) {
	got := openAIRealtimeURL(config.OpenAIConfig{
		Model:   "gpt-realtime",
		BaseURL: "wss://api.openai.com/v1/realtime",
	})
	if got != "wss://api.openai.com/v1/realtime?model=gpt-realtime" {
		t.Fatalf("openAIRealtimeURL = %q", got)
	}
}

func TestRunSession_WithInjectedSessionInferencer_UsesAgentLoopSessionPath(t *testing.T) {
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("session loop response")},
			{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		},
	}
	var out bytes.Buffer

	if err := RunSession(context.Background(), &out, SessionRunOptions{
		ReplayPath:        "synthetic.json",
		Prompt:            "hello session",
		SessionInferencer: sessionInf,
	}); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if !sessionInf.connected {
		t.Fatal("session command did not connect the configured session inferencer")
	}
	if got := out.String(); !strings.Contains(got, "session loop response") {
		t.Fatalf("session command did not print model deltas from Agent Loop, got:\n%s", got)
	}
}

func TestRunSession_OpenAIRealtimeRecordWithInjectedInferencer_UsesSessionPath(t *testing.T) {
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("openai realtime session response")},
			{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("scripted-session", "test complete")},
		},
	}
	var out bytes.Buffer

	if err := RunSession(context.Background(), &out, SessionRunOptions{
		RecordPath:        filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "sk-test-key",
		ConfigDir:         t.TempDir(),
		Prompt:            "hello realtime",
		SessionInferencer: sessionInf,
	}); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if !sessionInf.connected {
		t.Fatal("OpenAI realtime record path did not connect the configured session inferencer")
	}
	if got := out.String(); !strings.Contains(got, "openai realtime session response") {
		t.Fatalf("session command did not print OpenAI realtime deltas from Agent Loop, got:\n%s", got)
	}
}

func TestRunSession_SessionProviderCloseExitsPromptly(t *testing.T) {
	sessionInf := &closingSessionInferencer{}
	started := time.Now()

	if err := RunSession(context.Background(), io.Discard, SessionRunOptions{
		RecordPath:        filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "sk-test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: sessionInf,
	}); err != nil {
		t.Fatalf("RunSession: %v", err)
	}

	if !sessionInf.connected {
		t.Fatal("session command did not connect the configured session inferencer")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("session provider close should exit promptly, took %s", elapsed)
	}
}

func TestRunSession_OpenAISessionRejectsNonRealtimeModelBeforeDial(t *testing.T) {
	dialer := &failingDialer{}

	err := RunSession(context.Background(), io.Discard, SessionRunOptions{
		RecordPath:      filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-4o",
		APIKey:          "sk-test-key",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: dialer,
	})
	if err == nil {
		t.Fatal("expected non-realtime OpenAI model to be rejected")
	}
	if !strings.Contains(err.Error(), "not realtime-capable") {
		t.Fatalf("expected actionable realtime model error, got: %v", err)
	}
	if dialer.called {
		t.Fatal("OpenAI non-realtime model validation should fail before any live dial")
	}
}

func TestRunSession_RecordFlushesCaptureWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	recordPath := filepath.Join(t.TempDir(), "canceled-recording.json")
	dialer := &cancelingRecordDialer{
		conn: &cancelingRecordConn{
			cancel: cancel,
			close:  make(chan struct{}),
		},
	}

	var out bytes.Buffer
	err := RunSession(ctx, &out, SessionRunOptions{
		RecordPath:      recordPath,
		Provider:        config.ProviderGrok,
		Model:           "grok-record-test",
		APIKey:          "xai-test-key",
		ConfigDir:       t.TempDir(),
		Prompt:          "keep the recorded session open until cancellation",
		WebSocketDialer: dialer,
	})
	if err == nil {
		t.Fatal("expected canceled record session error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("record cancellation should preserve context.Canceled, got: %v", err)
	}

	raw, readErr := os.ReadFile(recordPath)
	if readErr != nil {
		t.Fatalf("record mode should flush capture on cancellation: %v", readErr)
	}
	if !json.Valid(raw) {
		t.Fatalf("record mode wrote invalid JSON capture:\n%s", string(raw))
	}

	capture, loadErr := gwtesting.LoadSessionCapture(recordPath)
	if loadErr != nil {
		t.Fatalf("load flushed capture: %v", loadErr)
	}
	if len(capture.Records) < 2 {
		t.Fatalf("capture should include observed inbound and outbound traffic, got %d records", len(capture.Records))
	}
	assertCapturedDirectionAndType(t, capture.Records, gwtesting.DirectionClientToServer, "session.update")
	assertCapturedDirectionAndType(t, capture.Records, gwtesting.DirectionServerToClient, "session.created")
}

func TestPlanSessionRuntime_GenericReplayHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	capturePath := filepath.Join(t.TempDir(), "timed-replay.session.json")
	writeGenericSessionCapture(t, capturePath, []gwtesting.CapturedSessionEvent{
		capturedStreamEvent(gwtesting.DirectionServerToClient, 1, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("before cancel")),
		capturedStreamEvent(gwtesting.DirectionServerToClient, 2, 200, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("after cancel")),
	})

	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		ReplayPath: capturePath,
	}, sessionRuntimeFactory{
		newReplayInferencer: func(path string) messages.SessionInferencer {
			return gwtesting.NewReplaySessionInferencer(path, gwtesting.WithReplayTiming())
		},
	})
	if err != nil {
		t.Fatalf("planSessionRuntimeWithFactory: %v", err)
	}

	out := &lockedBuffer{}
	errCh := make(chan error, 1)
	plan.loopOut = out
	plan.finalize = nil
	plan.loop.MaxDuration = time.Second
	go func() {
		errCh <- plan.run(ctx, io.Discard)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "before cancel") {
			cancel()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	err = <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("replay cancellation should preserve context.Canceled, got: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "before cancel") {
		t.Fatalf("replay output missing pre-cancellation delta, got %q", got)
	}
	if strings.Contains(got, "after cancel") {
		t.Fatalf("replay output should stop before later timed deltas after cancellation, got %q", got)
	}
}

func TestChatServiceRun_PropagatesBannerWriteError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	exec := agent.NewExecutor(nil, nil, stubInferencer{}, true)
	service := NewChatService(exec, flags.NewGlobalFlags(), flags.NewAskFlags())

	err := service.Run(
		context.Background(),
		strings.NewReader(""),
		failingWriter{err: errors.New("stdout closed")},
		io.Discard,
	)
	if err == nil {
		t.Fatal("expected banner write error, got nil")
	}
	if !strings.Contains(err.Error(), "write chat banner") {
		t.Fatalf("error = %v, want write chat banner context", err)
	}
}

func TestRunAgentLoopSession_ReturnsOnCleanDoneSignal(t *testing.T) {
	done := make(chan struct{})
	sessionInf := &scriptedSessionInferencer{
		afterEvents: func() {
			close(done)
		},
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("done signal response")},
		},
	}
	var out bytes.Buffer

	start := time.Now()
	err := runAgentLoopSession(context.Background(), &out, sessionInf, sessionLoopOptions{
		MaxDuration: time.Second,
		Done:        done,
		DoneErr: func() error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("session loop waited for timeout after clean done signal; elapsed=%s", elapsed)
	}
	if got := out.String(); !strings.Contains(got, "done signal response") {
		t.Fatalf("session loop did not drain output before returning, got:\n%s", got)
	}
}

func TestRunAgentLoopSession_TimeoutCancelsLoopWithoutCallerCancellationError(t *testing.T) {
	sessionInf := &scriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("timeout response")},
		},
	}
	var out bytes.Buffer

	start := time.Now()
	err := runAgentLoopSession(context.Background(), &out, sessionInf, sessionLoopOptions{
		MaxDuration: 75 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession timeout should not report caller cancellation: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("session loop timeout should return promptly; elapsed=%s", elapsed)
	}
	if !sessionInf.connected {
		t.Fatal("session loop timeout path did not connect the configured inferencer")
	}
}

func assertCapturedDirectionAndType(t *testing.T, records []gwtesting.CapturedSessionEvent, direction gwtesting.SessionEventDirection, eventType string) {
	t.Helper()
	for _, record := range records {
		if record.Direction == direction && record.Type == eventType {
			return
		}
	}
	t.Fatalf("capture missing %s %s record: %#v", direction, eventType, records)
}

func writeSessionConfigFile(t *testing.T, configDir string, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(strings.TrimSpace(yaml)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func writeGenericSessionCapture(t *testing.T, path string, records []gwtesting.CapturedSessionEvent) {
	t.Helper()

	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  sessionProviderGrok,
			Model: "grok-replay-test",
		},
		Session: gwtesting.SessionMetadata{
			ID:           "sess-replay-test",
			StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		},
		Records: records,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write replay capture: %v", err)
	}
}

func writeOpenAIReplayCapture(t *testing.T, path, sessionUpdate string) {
	t.Helper()
	writeSessionCapture(t, path, gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  sessionProviderOpenAI,
			Model: "gpt-realtime",
		},
		Session: gwtesting.SessionMetadata{ID: "sess-openai-replay-test"},
		Records: []gwtesting.CapturedSessionEvent{
			{
				Sequence:    1,
				Direction:   gwtesting.DirectionClientToServer,
				TimestampMs: 0,
				Type:        "session.update",
				PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(sessionUpdate),
			},
			{
				Sequence:    2,
				Direction:   gwtesting.DirectionServerToClient,
				TimestampMs: 1,
				Type:        "session.created",
				PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"session.created","session":{"id":"sess-openai-replay-test","model":"gpt-realtime"}}`),
			},
		},
	})
}

func writeSessionCapture(t *testing.T, path string, capture gwtesting.SessionCapture) {
	t.Helper()
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal session capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write session capture: %v", err)
	}
}

func capturedStreamEvent(direction gwtesting.SessionEventDirection, sequence int, timestampMs int64, msgType messages.StreamMessageType, value messages.StreamMessageValue) gwtesting.CapturedSessionEvent {
	payload, err := gwtesting.MarshalStreamMessage(messages.StreamMessage{
		Type:  msgType,
		Value: value,
	})
	if err != nil {
		panic(err)
	}
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: timestampMs,
		Type:        string(msgType),
		PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
		Payload:     payload,
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type stubRuntimeDialer struct {
	id string
}

func (d *stubRuntimeDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("unexpected dial")
}

type stubRecordingDialer struct {
	stubRuntimeDialer
}

func (d *stubRecordingDialer) FlushToFile(string) error {
	return nil
}

type stubReplayDialer struct {
	stubRuntimeDialer
	model string
	done  chan struct{}
	err   error
}

func (d *stubReplayDialer) Done() <-chan struct{} { return d.done }
func (d *stubReplayDialer) Err() error            { return d.err }
func (d *stubReplayDialer) Model() string         { return d.model }

type scriptedSessionInferencer struct {
	events      []messages.StreamMessage
	afterEvents func()
	connected   bool
}

func (s *scriptedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s.connected = true
	session := newScriptedSession()
	go func() {
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("scripted-session", "session"),
		})
		time.Sleep(150 * time.Millisecond)
		for _, evt := range s.events {
			session.recv.Write(ctx, evt)
		}
		if s.afterEvents != nil {
			s.afterEvents()
		}
	}()
	return session, nil
}

type scriptedSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once
}

func newScriptedSession() *scriptedSession {
	return &scriptedSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](32),
		done: make(chan struct{}),
	}
}

func (s *scriptedSession) Send(context.Context, messages.StreamMessage) bool {
	return true
}

func (s *scriptedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *scriptedSession) Done() <-chan struct{} {
	return s.done
}

func (s *scriptedSession) Close() error {
	s.once.Do(func() {
		close(s.done)
	})
	return nil
}

type closingSessionInferencer struct {
	connected bool
}

func (s *closingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	s.connected = true
	session := newScriptedSession()
	_ = session.Close()
	return session, nil
}

type cancelingRecordDialer struct {
	conn *cancelingRecordConn
}

var _ transport.Dialer = (*cancelingRecordDialer)(nil)

func (d *cancelingRecordDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type failingDialer struct {
	called bool
}

var _ transport.Dialer = (*failingDialer)(nil)

func (d *failingDialer) Dial(string, map[string]string) (transport.Conn, error) {
	d.called = true
	return nil, errors.New("dial should not be called")
}

type cancelingRecordConn struct {
	cancel context.CancelFunc
	close  chan struct{}
	once   sync.Once
	read   bool
}

var _ transport.Conn = (*cancelingRecordConn)(nil)

func (c *cancelingRecordConn) ReadMessage() (int, []byte, error) {
	if !c.read {
		c.read = true
		go func() {
			time.Sleep(25 * time.Millisecond)
			c.cancel()
		}()
		return 1, []byte(`{"type":"session.created","session_id":"sess-record-canceled","model":"grok-record-test"}`), nil
	}
	<-c.close
	return 0, nil, io.EOF
}

func (c *cancelingRecordConn) WriteMessage(int, []byte) error {
	return nil
}

func (c *cancelingRecordConn) Close() error {
	c.once.Do(func() {
		close(c.close)
	})
	return nil
}
