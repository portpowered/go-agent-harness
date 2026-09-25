package wire

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const liveRuntimeTestTimeout = 5 * time.Second

type capturingSessionProviders struct {
	configs []providers.SessionConfig
	result  messages.SessionInferencer
}

func (p *capturingSessionProviders) BuildSession(_ context.Context, config providers.SessionConfig) (messages.SessionInferencer, error) {
	p.configs = append(p.configs, config)
	return p.result, nil
}

type unusedDialer struct{}

func (unusedDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return nil, errors.New("dialer is not used by the capturing provider")
}

// scriptedLiveSession is a provider session double whose reaction to each
// client send is supplied by the test.
type scriptedLiveSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	close   sync.Once
	react   func(*scriptedLiveSession, messages.StreamMessage)
}

func newScriptedLiveSession(react func(*scriptedLiveSession, messages.StreamMessage)) *scriptedLiveSession {
	session := &scriptedLiveSession{receive: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{}), react: react}
	session.emit(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("scripted", "test")})
	return session
}

func (s *scriptedLiveSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	if s.react != nil {
		s.react(s, msg)
	}
	return true
}

func (s *scriptedLiveSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *scriptedLiveSession) Done() <-chan struct{} { return s.done }

func (s *scriptedLiveSession) Close() error {
	s.close.Do(func() { close(s.done) })
	return nil
}

func (s *scriptedLiveSession) emit(msgs ...messages.StreamMessage) {
	for _, msg := range msgs {
		s.receive.Write(context.Background(), msg)
	}
}

func (s *scriptedLiveSession) emitAssistantText(text string) {
	s.emit(
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)
}

type scriptedInferencer struct {
	session *scriptedLiveSession
	err     error
}

func (i scriptedInferencer) ConnectSession(context.Context) (messages.Session, error) {
	if i.err != nil {
		return nil, i.err
	}
	return i.session, nil
}

func newScriptedLiveService(inferencer messages.SessionInferencer) session.LiveService {
	return NewLiveService(LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
			return inferencer, nil
		},
		Clock:     time.Now,
		Scheduler: clock.Real{},
	})
}

func TestProviderInferencerFactoryProjectsLiveRequest(t *testing.T) {
	provider := &capturingSessionProviders{result: scriptedInferencer{}}
	dialer := unusedDialer{}
	var references []string
	factory := NewProviderInferencerFactory(ProviderInferenceDependencies{
		Providers: provider, Dialer: dialer,
		ToolDefinitions: []messages.ToolDefinition{{Name: "default_tool"}},
		Credentials: func(_ context.Context, reference string) (string, error) {
			references = append(references, reference)
			return "resolved-key", nil
		},
	})
	createResponse := true
	_, err := factory(context.Background(), session.LiveRequest{
		Provider: "openai", Model: "gpt-realtime", BaseURL: "wss://example.test", CredentialReference: "vault:1",
		Instructions: "be brief", Voice: "marin", InputTranscription: true, InputTranscriptionModel: "whisper",
		InputAudioFormat: "pcm16", InputAudioSampleRate: 24000, ClientOwnsAudioTurnBoundaries: true,
		TurnDetection: &session.LiveTurnDetection{Type: "server_vad", CreateResponse: &createResponse},
		Capabilities:  &session.LiveCapabilities{Definitions: []messages.ToolDefinition{{Name: "participant_tool"}}},
		Replay:        session.LiveReplayPolicy{OutputCapturePath: "capture.json", Timing: session.LiveReplayTimingStep},
	})
	if err != nil {
		t.Fatalf("build provider session: %v", err)
	}
	if len(references) != 1 || references[0] != "vault:1" {
		t.Fatalf("credential references = %v, want the opaque request reference once", references)
	}
	config := provider.configs[0]
	if config.APIKey != "resolved-key" || config.Provider != "openai" || config.Model != "gpt-realtime" || config.Voice != "marin" || config.Instructions != "be brief" {
		t.Fatalf("provider identity = %+v", config)
	}
	if config.WebSocketDialer != dialer || config.RecordPath != "capture.json" || config.ReplayTiming != "step" || !config.ClientOwnsAudioTurnBoundaries {
		t.Fatalf("provider transport policy = %+v", config)
	}
	if len(config.Tools) != 1 || config.Tools[0].Name != "participant_tool" {
		t.Fatalf("provider tools = %+v, want the participant capability surface", config.Tools)
	}
	if config.InputTranscription == nil || !config.InputTranscription.Enabled || config.InputTranscription.Model != "whisper" {
		t.Fatalf("input transcription = %+v", config.InputTranscription)
	}
	if config.TurnDetection == nil || config.TurnDetection.CreateResponse == &createResponse || !*config.TurnDetection.CreateResponse {
		t.Fatalf("turn detection = %+v, want an independent copy", config.TurnDetection)
	}
}

func TestProviderInferencerFactoryCredentialAdmission(t *testing.T) {
	provider := &capturingSessionProviders{result: scriptedInferencer{}}
	unresolved := NewProviderInferencerFactory(ProviderInferenceDependencies{Providers: provider})
	if _, err := unresolved(context.Background(), session.LiveRequest{CredentialReference: "vault:missing"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing resolver error = %v, want unavailable credential reference", err)
	}
	if _, err := unresolved(context.Background(), session.LiveRequest{Replay: session.LiveReplayPolicy{InputCapturePath: "replay.json", Kind: session.LiveReplayKindTurn}}); err != nil {
		t.Fatalf("offline replay without a credential: %v", err)
	}
	replay := provider.configs[len(provider.configs)-1]
	if replay.APIKey != "replay" || replay.ReplayPath != "replay.json" || !replay.SessionMessageReplay || replay.ReplayTiming != "fast" {
		t.Fatalf("offline replay config = %+v, want the non-secret replay sentinel", replay)
	}
	resolverErr := errors.New("vault sealed")
	failing := NewProviderInferencerFactory(ProviderInferenceDependencies{
		Providers:   provider,
		Credentials: func(context.Context, string) (string, error) { return "", resolverErr },
	})
	if _, err := failing(context.Background(), session.LiveRequest{CredentialReference: "vault:1"}); !errors.Is(err, resolverErr) {
		t.Fatalf("resolver error = %v, want %v", err, resolverErr)
	}
	if _, err := NewProviderInferencerFactory(ProviderInferenceDependencies{})(context.Background(), session.LiveRequest{}); err == nil {
		t.Fatal("factory without a provider service built a session")
	}
}

func TestRunLiveRecordsInvocationMetrics(t *testing.T) {
	provider := newScriptedLiveSession(func(s *scriptedLiveSession, msg messages.StreamMessage) {
		if msg.Type == messages.StreamTypeTextDelta {
			s.emitAssistantText("sunny")
		}
	})
	runner, ok := newScriptedLiveService(scriptedInferencer{session: provider}).(session.LiveRunner)
	if !ok {
		t.Fatal("live service does not own complete invocations")
	}
	recorder, err := metrics.NewInMemorySink()
	if err != nil {
		t.Fatalf("new metrics sink: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveRuntimeTestTimeout)
	defer cancel()
	err = runner.RunLive(ctx, session.LiveRunOptions{
		Request: session.LiveRequest{SessionID: "metrics", OpeningPrompt: "weather?", FinishAfterResponse: true},
		Metrics: recorder,
	})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	snapshot := recorder.Snapshot()
	if got := snapshot.SeriesFor(metrics.DirectionInput, metrics.ModalityText).TotalBytes; got != uint64(len("weather?")) {
		t.Fatalf("input text bytes = %d, want the opening prompt", got)
	}
	if got := snapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityText).TotalBytes; got != uint64(len("sunny")) {
		t.Fatalf("output text bytes = %d, want the assistant response", got)
	}
}

type recordingToolExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (e *recordingToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call.Name)
	e.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "done"}, nil
}

func (e *recordingToolExecutor) called(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, call := range e.calls {
		if call == name {
			return true
		}
	}
	return false
}

func TestLiveToolSurfaceFollowsRefreshedCapabilities(t *testing.T) {
	executor := &recordingToolExecutor{}
	var surface atomic.Value
	surface.Store([]messages.ToolDefinition{{Name: "stable_tool"}})
	provider := newScriptedLiveSession(func(s *scriptedLiveSession, msg messages.StreamMessage) {
		if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value.Content == "use the page tool" {
			s.emit(
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
				messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-page", "page_tool")},
				messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-page", "page_tool", `{}`)},
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			)
		}
	})
	handle, err := newScriptedLiveService(scriptedInferencer{session: provider}).OpenLive(context.Background(), session.LiveRequest{
		SessionID: "refresh",
		Capabilities: &session.LiveCapabilities{
			Executor: executor, Definitions: surface.Load().([]messages.ToolDefinition),
			RefreshDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
				return surface.Load().([]messages.ToolDefinition), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	defer func() { _ = handle.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), liveRuntimeTestTimeout)
	defer cancel()
	if err := handle.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	surface.Store([]messages.ToolDefinition{{Name: "stable_tool"}, {Name: "page_tool"}})
	if err := handle.Send(ctx, session.LiveControl{Kind: session.LiveControlText, Text: "use the page tool"}); err != nil {
		t.Fatalf("send text control: %v", err)
	}
	for !executor.called("page_tool") {
		select {
		case <-ctx.Done():
			t.Fatal("a tool published by a capability refresh was not executable")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestRunLiveReportsProviderConnectFailureOnce(t *testing.T) {
	connectErr := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	runner, ok := newScriptedLiveService(scriptedInferencer{err: connectErr}).(session.LiveRunner)
	if !ok {
		t.Fatal("live service does not own complete invocations")
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveRuntimeTestTimeout)
	defer cancel()
	err := runner.RunLive(ctx, session.LiveRunOptions{Request: session.LiveRequest{SessionID: "connect", OpeningPrompt: "hello", FinishAfterResponse: true}})
	if !errors.Is(err, connectErr) {
		t.Fatalf("RunLive error = %v, want provider connect failure", err)
	}
	if got := strings.Count(err.Error(), connectErr.Error()); got != 1 {
		t.Fatalf("connect failure rendered %d times: %q", got, err)
	}
}
