package agentruntime

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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	services "github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
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

	defaultDialer := &trackingRuntimeDialer{}
	var gotVoice string
	var gotCfg config.OpenAIConfig
	var gotDialer transport.Dialer

	plan, err := planSessionRuntimeWithFactory(withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:      filepath.Join(t.TempDir(), "openai.session.json"),
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-realtime",
		APIKey:          "sk-override-key",
		ConfigDir:       configDir,
		Voice:           "marin",
		WebSocketDialer: defaultDialer,
	}), sessionRuntimeFactory{
		newOpenAISessionInf: func(cfg config.OpenAIConfig, voice string, dialer transport.Dialer, _ models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
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
	if _, ok := gotDialer.(runtimerecording.Writer); !ok {
		t.Fatalf("OpenAI record inferencer received %T, want the public recording writer", gotDialer)
	}
	assertDialFailure(t, gotDialer, observedRuntimeDialError)
	if defaultDialer.calls != 1 {
		t.Fatalf("recording service forwarded %d dial attempts to the caller-owned websocket dialer, want 1", defaultDialer.calls)
	}
	if plan.flushCapture == nil {
		t.Fatal("OpenAI record plan has no capture finalizer")
	}
	if err := plan.flushCapture(); err != nil {
		t.Fatalf("flush provider capture: %v", err)
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
				factory.newOpenAISessionInf = func(config.OpenAIConfig, string, transport.Dialer, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
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
			}
			testCase.configure(&factory)

			plan, err := testCase.plan(withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
				RecordPath: recordPath,
				Provider:   testCase.provider,
				Model:      testCase.model,
				APIKey:     testCase.apiKey,
				ConfigDir:  t.TempDir(),
				AudioInputs: []ScheduledAudioInput{
					{AfterCompletedTurns: 0, PCM: []byte{1, 2}, EndOfTurn: true},
					{AfterCompletedTurns: 1, PCM: []byte{3, 4}, EndOfTurn: true},
				},
			}), factory)
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
			if want := testCase.name == "openai"; plan.loop.RequireSessionUpdated != want {
				t.Fatalf("scheduled %s configuration-ack requirement = %t, want %t", testCase.provider, plan.loop.RequireSessionUpdated, want)
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

	callerDialer := &trackingRuntimeDialer{}
	var defaultDialerCalled bool
	var gotCfg config.GrokConfig
	var gotDialer transport.Dialer

	plan, err := planSessionRuntimeWithFactory(withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:      filepath.Join(t.TempDir(), "grok.session.json"),
		Provider:        config.ProviderGrok,
		Model:           "grok-override-model",
		APIKey:          "xai-override-key",
		ConfigDir:       configDir,
		WebSocketDialer: callerDialer,
	}), sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer {
			defaultDialerCalled = true
			return &stubRuntimeDialer{id: "unexpected-default"}
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
	if _, ok := gotDialer.(runtimerecording.Writer); !ok {
		t.Fatalf("Grok session inferencer received %T, want the public recording writer", gotDialer)
	}
	assertDialFailure(t, gotDialer, observedRuntimeDialError)
	if callerDialer.calls != 1 {
		t.Fatalf("recording service forwarded %d dial attempts to caller-owned dialer, want 1", callerDialer.calls)
	}
	if plan.flushCapture == nil {
		t.Fatal("Grok record plan has no capture finalizer")
	}
	if err := plan.flushCapture(); err != nil {
		t.Fatalf("flush provider capture: %v", err)
	}
	if gotCfg.APIKey != "xai-override-key" || gotCfg.Model != "grok-override-model" {
		t.Fatalf("Grok config overrides were not resolved before runtime planning: %#v", gotCfg)
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestWrapSessionRuntimeErrorClassifiesQuotaMessage(t *testing.T) {
	err := wrapSessionRuntimeError(sessionRuntimePlan{mode: sessionRuntimeModeReplayOpenAI}, errors.New("session error: You have no credits remaining."))
	if err == nil || !strings.Contains(err.Error(), "classification=rate_limited") {
		t.Fatalf("quota runtime error = %v, want rate_limited classification", err)
	}
}

func TestWriteSessionToolAnnouncementEnumeratesCanonicalSurface(t *testing.T) {
	var out bytes.Buffer
	writeSessionToolAnnouncement(&out, []messages.ToolDefinition{
		{Name: "write_file"},
		{Name: "exec"},
		{Name: "read_file"},
		{Name: "exec"},
	})
	if got, want := out.String(), "Tools: exec, read_file, write_file\n"; got != want {
		t.Fatalf("tool startup announcement = %q, want %q", got, want)
	}

	out.Reset()
	writeSessionToolAnnouncement(&out, nil)
	if got, want := out.String(), "Tools: none\n"; got != want {
		t.Fatalf("empty tool startup announcement = %q, want %q", got, want)
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

	if err := RunSession(context.Background(), &out, SessionRunOptions{AudioService: newTestAudioIOService(), ModelCatalog: testModelCatalog(),
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

	if err := RunSession(context.Background(), &out, withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:        filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "sk-test-key",
		ConfigDir:         t.TempDir(),
		Prompt:            "hello realtime",
		SessionInferencer: sessionInf,
	})); err != nil {
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

	if err := RunSession(context.Background(), io.Discard, withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:        filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:          config.ProviderOpenAI,
		Model:             "gpt-realtime",
		APIKey:            "sk-test-key",
		ConfigDir:         t.TempDir(),
		SessionInferencer: sessionInf,
	})); err != nil {
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

	err := RunSession(context.Background(), io.Discard, withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:      filepath.Join(t.TempDir(), "openai-session.json"),
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-4o",
		APIKey:          "sk-test-key",
		ConfigDir:       t.TempDir(),
		WebSocketDialer: dialer,
	}))
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
	err := RunSession(ctx, &out, withTestRecordingServices(SessionRunOptions{ModelCatalog: testModelCatalog(),
		RecordPath:      recordPath,
		Provider:        config.ProviderGrok,
		Model:           "grok-record-test",
		APIKey:          "xai-test-key",
		ConfigDir:       t.TempDir(),
		Prompt:          "keep the recorded session open until cancellation",
		WebSocketDialer: dialer,
	}))
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

func TestSessionRunTerminationErrorPreservesCallerCancellationAfterCleanLoopExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sessionRunTerminationError(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("clean loop termination after caller cancellation = %v, want context.Canceled", err)
	}
}

func TestSessionRunTerminationErrorPreservesCleanupFailure(t *testing.T) {
	wantErr := errors.New("session cleanup failed")
	if err := sessionRunTerminationError(context.Background(), wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("cleanup failure = %v, want %v", err, wantErr)
	}
}

func TestChatServiceRun_PropagatesBannerWriteError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	exec := sessionwire.NewService(sessionwire.Dependencies{Inferencer: stubInferencer{}, RelaxValidation: true})
	service := services.NewChatService(exec, flags.NewGlobalFlags(), flags.NewAskFlags())

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
		audioService: newTestAudioIOService(),
		MaxDuration:  time.Second,
		Done:         done,
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
		audioService: newTestAudioIOService(),
		MaxDuration:  75 * time.Millisecond,
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

type trackingRuntimeDialer struct{ calls int }

func (d *trackingRuntimeDialer) Dial(string, map[string]string) (transport.Conn, error) {
	d.calls++
	return nil, observedRuntimeDialError
}

type replayHandshakeRecordingDialer struct {
	conn *replayHandshakeRecordingConn
}

func (d *replayHandshakeRecordingDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type replayHandshakeRecordingConn struct {
	mu     sync.Mutex
	writes [][]byte
}

func (c *replayHandshakeRecordingConn) ReadMessage() (int, []byte, error) {
	return 0, nil, io.EOF
}

func (c *replayHandshakeRecordingConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *replayHandshakeRecordingConn) Close() error { return nil }

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
