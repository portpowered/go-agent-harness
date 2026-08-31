package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionAudioInterruptReadTool  = "read_cube_state"
	sessionAudioInterruptQueueTool = "queue_cube_moves"
)

var (
	sessionAudioInterruptScheduledPCM = []byte{0x11, 0x00, 0x12, 0x00}
	sessionAudioInterruptOverlapPCM   = []byte{0x91, 0x00, 0x92, 0x00}
)

type sessionAudioInterruptScenario string

const (
	sessionAudioInterruptUnfiltered sessionAudioInterruptScenario = "unfiltered"
	sessionAudioInterruptNamed      sessionAudioInterruptScenario = "named"
	sessionAudioInterruptNegative   sessionAudioInterruptScenario = "negative"
)

// TestSessionCommandAudioInterruptOrdering exercises the command composition
// used by the shipped session path: the real OpenAI realtime provider adapter,
// the real session loop, the composed WebMCP executor, and the broker's
// semantic watch. The provider and browser are both deterministic fixtures.
func TestSessionCommandAudioInterruptOrdering(t *testing.T) {
	for _, scenario := range []sessionAudioInterruptScenario{
		sessionAudioInterruptUnfiltered,
		sessionAudioInterruptNamed,
		sessionAudioInterruptNegative,
	} {
		t.Run(string(scenario), func(t *testing.T) {
			runSessionCommandAudioInterruptScenario(t, scenario)
		})
	}
}

func runSessionCommandAudioInterruptScenario(t *testing.T, scenario sessionAudioInterruptScenario) {
	t.Helper()
	broker, targetSession, refs := newSessionAudioInterruptFixture(t)
	defer func() { _ = broker.Close() }()
	targetSession.BlockInvocations()

	ledger := &sessionAudioInterruptEventLedger{}
	toolSet := webmcpTools.NewBrokerToolSet(broker)
	executor := &sessionAudioInterruptRecordingExecutor{inner: toolSet.Executor()}
	wire := newSessionAudioInterruptWire(scenario, refs)
	inferencer, err := services.NewOpenAIRealtimeSessionInferencerWithToolsAndOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime-2.1-mini"},
		toolSet.Definitions(),
		oaiprovider.WithWebSocketDialer(sessionAudioInterruptDialer{wire: wire}),
		oaiprovider.WithClientOwnedAudioTurnBoundaries(),
	)
	if err != nil {
		t.Fatalf("build realtime inferencer: %v", err)
	}

	tempDir := t.TempDir()
	scheduledPath := filepath.Join(tempDir, "scheduled.raw")
	interruptPath := filepath.Join(tempDir, "interrupt.raw")
	if err := os.WriteFile(scheduledPath, sessionAudioInterruptScheduledPCM, 0o600); err != nil {
		t.Fatalf("write scheduled audio fixture: %v", err)
	}
	if err := os.WriteFile(interruptPath, sessionAudioInterruptOverlapPCM, 0o600); err != nil {
		t.Fatalf("write interruption audio fixture: %v", err)
	}

	releaseErr := make(chan error, 1)
	go releaseSessionAudioInterruptInvocations(t, scenario, targetSession, wire, releaseErr)

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = filepath.Join(tempDir, "config")
	capabilityFactory := func(*config.Config) (SessionToolCapabilities, error) {
		return SessionToolCapabilities{
			Executor:    executor,
			Definitions: toolSet.Definitions(),
			BrowserWatch: func(ctx context.Context) <-chan webmcp.BrokerEvent {
				return ledger.watch(ctx, broker)
			},
			Close: broker.Close,
		}, nil
	}
	commandOwner := NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(
		flags.NewAskFlags(),
		globalFlags,
		nil,
		inferencer,
		nil,
		nil,
		capabilityFactory,
		nil,
	)
	command := commandOwner.Generate()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs(sessionAudioInterruptArgs(scenario, scheduledPath, interruptPath, filepath.Join(tempDir, "recording")))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session command: %v\nprovider writes: %s\nprovider protocol error: %v\nprovider events: %v\ntool calls: %#v\ncommand output: %s\nbrowser invocations: %#v\nbroker events: %#v", err, wire.writeSummary(), wire.protocolError(), wire.eventsSnapshot(), executor.callsSnapshot(), output.String(), targetSession.Invocations(), ledger.eventsSnapshot())
	}
	select {
	case err := <-releaseErr:
		if err != nil {
			t.Fatalf("release browser invocation: %v", err)
		}
	default:
	}
	if err := wire.protocolError(); err != nil {
		t.Fatalf("scripted provider protocol: %v", err)
	}

	assertSessionAudioInterruptScenario(t, scenario, wire.writesSnapshot(), ledger.eventsSnapshot())
}

func sessionAudioInterruptArgs(scenario sessionAudioInterruptScenario, scheduledPath, interruptPath, recordDir string) []string {
	args := []string{
		"--browser-tools", "webmcp",
		"--provider", "openai",
		"--model", "gpt-realtime-2.1-mini",
		"--api-key", "test-key",
		"--record-dir", recordDir,
		"--audio-in-turn", scheduledPath,
		"--audio-interrupt", interruptPath,
	}
	if scenario != sessionAudioInterruptUnfiltered {
		args = append(args, "--audio-interrupt-on-tool", sessionAudioInterruptQueueTool)
	}
	return args
}

func releaseSessionAudioInterruptInvocations(
	t *testing.T,
	scenario sessionAudioInterruptScenario,
	targetSession *testkit.ScriptedTargetSession,
	wire *sessionAudioInterruptWire,
	result chan<- error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var record testkit.InvocationRecord
	for {
		nextRecord, err := targetSession.WaitForInvocation(ctx)
		if err != nil {
			result <- err
			return
		}
		record = nextRecord
		if scenario == sessionAudioInterruptNegative {
			result <- targetSession.ReleaseInvocation(record.ID, json.RawMessage(`{"state":"read"}`))
			return
		}
		if scenario == sessionAudioInterruptNamed && record.ToolName == sessionAudioInterruptReadTool {
			if err := targetSession.ReleaseInvocation(record.ID, json.RawMessage(`{"state":"read"}`)); err != nil {
				result <- err
				return
			}
			continue
		}
		if record.ToolName != sessionAudioInterruptQueueTool {
			result <- fmt.Errorf("blocked invocation = %#v, want %q", record, sessionAudioInterruptQueueTool)
			return
		}
		break
	}

	select {
	case <-wire.interruptCommitSeen():
	case <-ctx.Done():
		result <- fmt.Errorf("wait for interruption commit: %w", ctx.Err())
		return
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		result <- fmt.Errorf("hold browser invocation: %w", ctx.Err())
		return
	}
	result <- targetSession.ReleaseInvocation(record.ID, json.RawMessage(`{"state":"queued"}`))
}

func newSessionAudioInterruptFixture(t *testing.T) (*webmcp.StatefulBroker, *testkit.ScriptedTargetSession, map[string]webmcp.ToolRef) {
	t.Helper()
	clock := testkit.NewFakeClock(time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC))
	ids := testkit.NewDeterministicIDs()
	candidate := webmcp.BrowserCandidate{ID: "browser-s2s-interrupt", Product: "scripted", Loopback: true}
	target := webmcp.Target{
		BrowserID: candidate.ID,
		ID:        "tab-s2s-interrupt",
		Type:      "page",
		Title:     "S2S interruption fixture",
		URL:       "https://s2s-interrupt.test/",
		Origin:    "https://s2s-interrupt.test",
	}
	readOnly := true
	writeTool := false
	runtime := testkit.NewScriptedBrowserRuntimeWithOptions(
		testkit.RuntimeOptions{Clock: clock, IDs: ids},
		testkit.BrowserConfig{
			Candidate: candidate,
			Targets: []testkit.TargetConfig{testkit.NewTargetConfig(
				target,
				testkit.WithInitialCatalog(
					webmcp.ToolDescriptor{
						Name:        sessionAudioInterruptReadTool,
						Description: "Read the deterministic cube state.",
						FrameID:     "frame-cube",
						InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
						Annotations: webmcp.ToolAnnotations{ReadOnly: &readOnly},
					},
					webmcp.ToolDescriptor{
						Name:        sessionAudioInterruptQueueTool,
						Description: "Queue deterministic cube moves.",
						FrameID:     "frame-cube",
						InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
						Annotations: webmcp.ToolAnnotations{ReadOnly: &writeTool},
					},
				),
			)},
		},
	)
	broker := webmcp.NewBroker(webmcp.BrokerOptions{
		Runtime:           runtime,
		Discoverer:        sessionAudioInterruptDiscoverer{candidate: candidate},
		IDs:               ids,
		Clock:             clock,
		InvocationTimeout: 2 * time.Second,
	})
	if _, err := broker.Select(context.Background(), webmcp.TargetSelector{BrowserID: candidate.ID, TargetID: target.ID}); err != nil {
		t.Fatalf("select scripted WebMCP target: %v", err)
	}
	handleValue, err := runtime.Open(context.Background(), candidate)
	if err != nil {
		t.Fatalf("open scripted WebMCP browser: %v", err)
	}
	session := handleValue.(*testkit.ScriptedBrowserHandle).TargetSession(target.ID)
	if session == nil {
		t.Fatal("scripted WebMCP target session is nil")
	}
	catalog, err := broker.ListTools(context.Background(), webmcp.ListToolsOptions{IncludeSchemas: true})
	if err != nil {
		t.Fatalf("list scripted WebMCP tools: %v", err)
	}
	refs := make(map[string]webmcp.ToolRef, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		refs[tool.Name] = tool.Ref
	}
	for _, name := range []string{sessionAudioInterruptReadTool, sessionAudioInterruptQueueTool} {
		if refs[name] == "" {
			t.Fatalf("catalog tool %q has no session ref: %#v", name, catalog.Tools)
		}
	}
	return broker, session, refs
}

type sessionAudioInterruptDiscoverer struct {
	candidate webmcp.BrowserCandidate
}

func (d sessionAudioInterruptDiscoverer) Discover(ctx context.Context, _ webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return []webmcp.BrowserCandidate{d.candidate}, nil
	}
}

type sessionAudioInterruptEventObservation struct {
	event webmcp.BrokerEvent
	at    time.Time
}

type sessionAudioInterruptRecordingExecutor struct {
	mu    sync.Mutex
	inner messages.ToolExecutor
	calls []messages.ToolCall
}

func (e *sessionAudioInterruptRecordingExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return e.inner.Execute(ctx, call)
}

func (e *sessionAudioInterruptRecordingExecutor) callsSnapshot() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

type sessionAudioInterruptEventLedger struct {
	mu     sync.Mutex
	events []sessionAudioInterruptEventObservation
}

func (l *sessionAudioInterruptEventLedger) watch(ctx context.Context, broker webmcp.Broker) <-chan webmcp.BrokerEvent {
	if ctx == nil {
		ctx = context.Background()
	}
	source := broker.Watch(ctx)
	out := make(chan webmcp.BrokerEvent, 64)
	go func() {
		defer close(out)
		for {
			select {
			case event, ok := <-source:
				if !ok {
					return
				}
				l.mu.Lock()
				l.events = append(l.events, sessionAudioInterruptEventObservation{event: event, at: time.Now()})
				l.mu.Unlock()
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (l *sessionAudioInterruptEventLedger) eventsSnapshot() []sessionAudioInterruptEventObservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sessionAudioInterruptEventObservation(nil), l.events...)
}

type sessionAudioInterruptInvocationSpan struct {
	start    sessionAudioInterruptEventObservation
	terminal sessionAudioInterruptEventObservation
}

func findSessionAudioInterruptInvocationSpan(t *testing.T, events []sessionAudioInterruptEventObservation, name string) sessionAudioInterruptInvocationSpan {
	t.Helper()
	for index, observation := range events {
		event := observation.event
		if event.Type != webmcp.BrokerEventInvocationCreated || event.State != webmcp.InvocationDispatched || event.ToolName != name || event.InvocationID == "" {
			continue
		}
		for _, terminal := range events[index+1:] {
			if terminal.event.Type == webmcp.BrokerEventInvocationTerminal && terminal.event.InvocationID == event.InvocationID {
				return sessionAudioInterruptInvocationSpan{start: observation, terminal: terminal}
			}
		}
		t.Fatalf("dispatched %q invocation %q had no terminal broker event; events=%#v", name, event.InvocationID, events)
	}
	t.Fatalf("no dispatched %q invocation in broker events: %#v", name, events)
	return sessionAudioInterruptInvocationSpan{}
}

func assertSessionAudioInterruptScenario(t *testing.T, scenario sessionAudioInterruptScenario, writes []sessionAudioInterruptWireWrite, events []sessionAudioInterruptEventObservation) {
	t.Helper()
	commits := sessionAudioInterruptCommitGroups(t, writes)
	if len(commits) == 0 {
		t.Fatalf("provider emitted no audio commits; writes=%s", sessionAudioInterruptWriteSummary(writes))
	}
	if len(commits[0].appends) == 0 {
		t.Fatalf("first audio commit had no nonempty append group; writes=%s", sessionAudioInterruptWriteSummary(writes))
	}
	if !sessionAudioInterruptGroupHasPrefix(commits[0].appends, sessionAudioInterruptScheduledPCM) {
		t.Fatalf("first audio commit did not contain scheduled audio; commits=%#v", commits)
	}

	var targetSpan sessionAudioInterruptInvocationSpan
	switch scenario {
	case sessionAudioInterruptUnfiltered:
		targetSpan = findSessionAudioInterruptInvocationSpan(t, events, sessionAudioInterruptQueueTool)
		if len(commits) != 2 {
			t.Fatalf("unfiltered provider commits = %d, want exactly scheduled + interruption; commits=%#v", len(commits), commits)
		}
		assertSessionAudioInterruptCommitInSpan(t, commits[1], targetSpan)
		if !sessionAudioInterruptGroupHasPrefix(commits[1].appends, sessionAudioInterruptOverlapPCM) {
			t.Fatalf("unfiltered interruption commit did not contain overlap audio; commits=%#v", commits)
		}
		if duration := targetSpan.terminal.at.Sub(targetSpan.start.at); duration < 250*time.Millisecond {
			t.Fatalf("unfiltered browser invocation in-flight duration = %s, want controlled ~300ms window", duration)
		}
	case sessionAudioInterruptNamed:
		readSpan := findSessionAudioInterruptInvocationSpan(t, events, sessionAudioInterruptReadTool)
		targetSpan = findSessionAudioInterruptInvocationSpan(t, events, sessionAudioInterruptQueueTool)
		if len(commits) != 2 {
			t.Fatalf("named provider commits = %d, want exactly scheduled + matching interruption; commits=%#v", len(commits), commits)
		}
		if !readSpan.terminal.at.Before(targetSpan.start.at) {
			t.Fatalf("named nonmatching invocation did not finish before matching invocation: read=%s..%s matching=%s", readSpan.start.at, readSpan.terminal.at, targetSpan.start.at)
		}
		assertSessionAudioInterruptCommitInSpan(t, commits[1], targetSpan)
		if !sessionAudioInterruptGroupHasPrefix(commits[1].appends, sessionAudioInterruptOverlapPCM) {
			t.Fatalf("named interruption commit did not contain overlap audio; commits=%#v", commits)
		}
	case sessionAudioInterruptNegative:
		readSpan := findSessionAudioInterruptInvocationSpan(t, events, sessionAudioInterruptReadTool)
		if len(commits) != 1 {
			t.Fatalf("negative named provider commits = %d, want only scheduled commit; commits=%#v", len(commits), commits)
		}
		if _, ok := sessionAudioInterruptFindSpan(events, sessionAudioInterruptQueueTool); ok {
			t.Fatalf("negative named scenario unexpectedly invoked %q; events=%#v", sessionAudioInterruptQueueTool, events)
		}
		for _, commit := range commits {
			if sessionAudioInterruptGroupHasPrefix(commit.appends, sessionAudioInterruptOverlapPCM) {
				t.Fatalf("negative named scenario emitted interruption audio; commits=%#v", commits)
			}
		}
		if !readSpan.terminal.at.After(readSpan.start.at) {
			t.Fatalf("negative read invocation did not have a positive in-flight span: %#v", readSpan)
		}
	}
}

func assertSessionAudioInterruptCommitInSpan(t *testing.T, commit sessionAudioInterruptCommitGroup, span sessionAudioInterruptInvocationSpan) {
	t.Helper()
	if !span.start.at.Before(commit.at) || !commit.at.Before(span.terminal.at) {
		t.Fatalf("interrupt commit was not causally inside browser invocation: start=%s commit=%s terminal=%s", span.start.at, commit.at, span.terminal.at)
	}
}

func sessionAudioInterruptFindSpan(events []sessionAudioInterruptEventObservation, name string) (sessionAudioInterruptInvocationSpan, bool) {
	for index, observation := range events {
		event := observation.event
		if event.Type != webmcp.BrokerEventInvocationCreated || event.State != webmcp.InvocationDispatched || event.ToolName != name || event.InvocationID == "" {
			continue
		}
		for _, terminal := range events[index+1:] {
			if terminal.event.Type == webmcp.BrokerEventInvocationTerminal && terminal.event.InvocationID == event.InvocationID {
				return sessionAudioInterruptInvocationSpan{start: observation, terminal: terminal}, true
			}
		}
	}
	return sessionAudioInterruptInvocationSpan{}, false
}

type sessionAudioInterruptCommitGroup struct {
	at      time.Time
	appends []sessionAudioInterruptAudioAppend
}

type sessionAudioInterruptAudioAppend struct {
	at    time.Time
	bytes []byte
}

func sessionAudioInterruptCommitGroups(t *testing.T, writes []sessionAudioInterruptWireWrite) []sessionAudioInterruptCommitGroup {
	t.Helper()
	var pending []sessionAudioInterruptAudioAppend
	commits := make([]sessionAudioInterruptCommitGroup, 0, 2)
	for _, write := range writes {
		switch write.Type {
		case "input_audio_buffer.append":
			var event struct {
				Audio string `json:"audio"`
			}
			if err := json.Unmarshal(write.Payload, &event); err != nil {
				t.Fatalf("decode audio append: %v", err)
			}
			if event.Audio == "" {
				t.Fatalf("audio append was empty: %s", write.Payload)
			}
			decoded, err := base64.StdEncoding.DecodeString(event.Audio)
			if err != nil {
				t.Fatalf("decode audio append: %v", err)
			}
			if len(decoded) == 0 {
				t.Fatalf("decoded audio append was empty: %s", write.Payload)
			}
			pending = append(pending, sessionAudioInterruptAudioAppend{at: write.at, bytes: decoded})
		case "input_audio_buffer.commit":
			commits = append(commits, sessionAudioInterruptCommitGroup{at: write.at, appends: append([]sessionAudioInterruptAudioAppend(nil), pending...)})
			pending = nil
		}
	}
	if len(pending) > 0 {
		t.Fatalf("provider left an uncommitted audio append group: %#v", pending)
	}
	return commits
}

func sessionAudioInterruptGroupHasPrefix(appends []sessionAudioInterruptAudioAppend, prefix []byte) bool {
	for _, append := range appends {
		if bytes.Contains(append.bytes, prefix) {
			return true
		}
	}
	return false
}

type sessionAudioInterruptWireWrite struct {
	at      time.Time
	Type    string
	Payload json.RawMessage
}

type sessionAudioInterruptDialer struct {
	wire *sessionAudioInterruptWire
}

func (d sessionAudioInterruptDialer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	return d.wire, nil
}

type sessionAudioInterruptPageCall struct {
	id                    string
	toolName              string
	resultReceived        bool
	acknowledgementSent   bool
	interruptResponseSent bool
}

type sessionAudioInterruptWire struct {
	mu      sync.Mutex
	inbound chan []byte
	done    chan struct{}
	close   sync.Once

	scenario sessionAudioInterruptScenario
	refs     map[string]webmcp.ToolRef
	writes   []sessionAudioInterruptWireWrite
	events   []string

	protocolErr error
	handshake   bool
	toolIndex   int
	callIndex   int
	active      *sessionAudioInterruptPageCall
	interrupts  int
	finalSent   bool
	audioGroup  [][]byte
	interruptCh chan struct{}
}

func newSessionAudioInterruptWire(scenario sessionAudioInterruptScenario, refs map[string]webmcp.ToolRef) *sessionAudioInterruptWire {
	return &sessionAudioInterruptWire{
		inbound:     make(chan []byte, 128),
		done:        make(chan struct{}),
		scenario:    scenario,
		refs:        refs,
		interruptCh: make(chan struct{}, 1),
	}
}

func (w *sessionAudioInterruptWire) WriteMessage(_ int, payload []byte) error {
	select {
	case <-w.done:
		return io.ErrClosedPipe
	default:
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		w.fail(fmt.Errorf("decode client event: %w", err))
		return err
	}
	w.mu.Lock()
	w.writes = append(w.writes, sessionAudioInterruptWireWrite{at: time.Now(), Type: envelope.Type, Payload: append(json.RawMessage(nil), payload...)})
	w.mu.Unlock()

	switch envelope.Type {
	case "session.update":
		w.sendHandshakeOnce()
	case "input_audio_buffer.append":
		w.recordAudioAppend(payload)
	case "input_audio_buffer.commit":
		w.recordAudioCommit()
	case "conversation.item.create":
		w.acceptToolResult(payload)
	case "response.create":
		w.acceptResponseCreate()
	case "response.cancel":
		// The client may cancel an active provider response before sending the
		// overlap audio. It is a valid realtime control boundary for this fixture.
	default:
		w.fail(fmt.Errorf("unexpected client event %q", envelope.Type))
	}
	return nil
}

func (w *sessionAudioInterruptWire) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-w.inbound:
		return 1, payload, nil
	case <-w.done:
		return 0, nil, io.EOF
	}
}

func (w *sessionAudioInterruptWire) Close() error {
	w.close.Do(func() { close(w.done) })
	return nil
}

func (w *sessionAudioInterruptWire) enqueue(event map[string]any) {
	var eventType string
	if value, ok := event["type"].(string); ok {
		eventType = value
	}
	w.mu.Lock()
	w.events = append(w.events, eventType)
	w.mu.Unlock()
	payload, err := json.Marshal(event)
	if err != nil {
		w.fail(fmt.Errorf("encode server event: %w", err))
		return
	}
	select {
	case w.inbound <- payload:
	case <-w.done:
	}
}

func (w *sessionAudioInterruptWire) sendHandshakeOnce() {
	w.mu.Lock()
	if w.handshake {
		w.mu.Unlock()
		return
	}
	w.handshake = true
	w.mu.Unlock()
	w.enqueue(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess-s2s-interrupt", "model": "gpt-realtime-2.1-mini"}})
	w.enqueue(map[string]any{"type": "session.updated", "session": map[string]any{"id": "sess-s2s-interrupt", "model": "gpt-realtime-2.1-mini"}})
}

func (w *sessionAudioInterruptWire) recordAudioAppend(payload []byte) {
	var event struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		w.fail(fmt.Errorf("decode audio append: %w", err))
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Audio)
	if err != nil {
		w.fail(fmt.Errorf("decode audio append: %w", err))
		return
	}
	w.mu.Lock()
	w.audioGroup = append(w.audioGroup, decoded)
	w.mu.Unlock()
}

func (w *sessionAudioInterruptWire) recordAudioCommit() {
	w.mu.Lock()
	group := append([][]byte(nil), w.audioGroup...)
	w.audioGroup = nil
	if sessionAudioInterruptWireGroupHasPrefix(group, sessionAudioInterruptOverlapPCM) {
		w.interrupts++
	}
	isInterrupt := sessionAudioInterruptWireGroupHasPrefix(group, sessionAudioInterruptOverlapPCM)
	w.mu.Unlock()
	if isInterrupt {
		select {
		case w.interruptCh <- struct{}{}:
		default:
		}
	}
}

func sessionAudioInterruptWireGroupHasPrefix(group [][]byte, prefix []byte) bool {
	for _, payload := range group {
		if bytes.Contains(payload, prefix) {
			return true
		}
	}
	return false
}

func (w *sessionAudioInterruptWire) interruptCommitSeen() <-chan struct{} {
	return w.interruptCh
}

func (w *sessionAudioInterruptWire) acceptToolResult(payload []byte) {
	var event struct {
		Item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		w.fail(fmt.Errorf("decode tool result: %w", err))
		return
	}
	if event.Item.Type != "function_call_output" {
		w.fail(fmt.Errorf("tool result item type = %q, want function_call_output", event.Item.Type))
		return
	}
	envelope, err := webmcp.UnmarshalToolResult([]byte(event.Item.Output))
	if err != nil {
		w.fail(fmt.Errorf("decode tool result %q: %w", event.Item.CallID, err))
		return
	}
	if !envelope.OK {
		w.fail(fmt.Errorf("tool result %q was unsuccessful: %#v", event.Item.CallID, envelope))
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active == nil || w.active.id != event.Item.CallID {
		w.failLocked(fmt.Errorf("tool result %q did not match active call %#v", event.Item.CallID, w.active))
		return
	}
	if w.active.resultReceived {
		w.failLocked(fmt.Errorf("tool result %q was delivered twice", event.Item.CallID))
		return
	}
	w.active.resultReceived = true
}

func (w *sessionAudioInterruptWire) acceptResponseCreate() {
	w.mu.Lock()
	active := w.active
	interrupts := w.interrupts
	if active != nil && !active.resultReceived {
		if !active.acknowledgementSent {
			active.acknowledgementSent = true
			w.mu.Unlock()
			w.sendToolAcknowledgementResponse(active.id)
			return
		}
		if interrupts > 0 && !active.interruptResponseSent {
			active.interruptResponseSent = true
			w.mu.Unlock()
			w.sendInterruptResponse(active.id)
			return
		}
		w.failLocked(fmt.Errorf("response.create arrived while tool call %q was in flight after all expected control responses", active.id))
		w.mu.Unlock()
		return
	}

	if active != nil {
		w.active = nil
	}
	if w.finalSent {
		w.failLocked(errors.New("duplicate response.create after final response"))
		w.mu.Unlock()
		return
	}
	if w.toolIndex < len(w.toolSequence()) {
		name := w.toolSequence()[w.toolIndex]
		w.toolIndex++
		w.callIndex++
		call := sessionAudioInterruptPageCall{id: fmt.Sprintf("call-s2s-page-%d", w.callIndex), toolName: name}
		w.active = &call
		w.mu.Unlock()
		w.sendPageTool(call)
		return
	}
	w.finalSent = true
	w.mu.Unlock()
	w.sendFinalResponse()
}

func (w *sessionAudioInterruptWire) toolSequence() []string {
	switch w.scenario {
	case sessionAudioInterruptNamed:
		return []string{sessionAudioInterruptReadTool, sessionAudioInterruptQueueTool}
	case sessionAudioInterruptNegative:
		return []string{sessionAudioInterruptReadTool}
	default:
		return []string{sessionAudioInterruptQueueTool}
	}
}

func (w *sessionAudioInterruptWire) sendPageTool(call sessionAudioInterruptPageCall) {
	ref := w.refs[call.toolName]
	responseID := "response-" + call.id
	arguments, _ := json.Marshal(map[string]string{
		"tool_ref":   string(ref),
		"input_json": `{}`,
		"reason":     "s2s interruption ordering fixture",
	})
	w.enqueue(map[string]any{"type": "response.created", "response": map[string]any{"id": responseID}})
	w.enqueue(map[string]any{
		"type":        "response.output_item.added",
		"response_id": responseID,
		"item": map[string]any{
			"type":      "function_call",
			"id":        "item-" + call.id,
			"call_id":   call.id,
			"name":      webmcp.InvokeToolName,
			"arguments": "",
		},
	})
	w.enqueue(map[string]any{
		"type":        "response.function_call_arguments.done",
		"response_id": responseID,
		"call_id":     call.id,
		"name":        webmcp.InvokeToolName,
		"arguments":   string(arguments),
	})
	w.enqueue(map[string]any{"type": "response.done", "response_id": responseID, "response": map[string]any{"id": responseID, "status": "completed"}})
}

func (w *sessionAudioInterruptWire) sendInterruptResponse(callID string) {
	responseID := "response-interrupt-" + callID
	w.enqueue(map[string]any{"type": "response.created", "response": map[string]any{"id": responseID}})
	w.enqueue(map[string]any{"type": "response.output_text.delta", "response_id": responseID, "delta": "interrupt acknowledged"})
	w.enqueue(map[string]any{"type": "response.output_text.done", "response_id": responseID})
	w.enqueue(map[string]any{"type": "response.done", "response_id": responseID, "response": map[string]any{"id": responseID, "status": "completed"}})
}

func (w *sessionAudioInterruptWire) sendToolAcknowledgementResponse(callID string) {
	responseID := "response-ack-" + callID
	w.enqueue(map[string]any{"type": "response.created", "response": map[string]any{"id": responseID}})
	w.enqueue(map[string]any{"type": "response.output_text.delta", "response_id": responseID, "delta": "tool acknowledged"})
	w.enqueue(map[string]any{"type": "response.output_text.done", "response_id": responseID})
	w.enqueue(map[string]any{"type": "response.done", "response_id": responseID, "response": map[string]any{"id": responseID, "status": "completed"}})
}

func (w *sessionAudioInterruptWire) sendFinalResponse() {
	responseID := "response-final"
	w.enqueue(map[string]any{"type": "response.created", "response": map[string]any{"id": responseID}})
	w.enqueue(map[string]any{"type": "response.output_text.delta", "response_id": responseID, "delta": "fixture complete"})
	w.enqueue(map[string]any{"type": "response.output_text.done", "response_id": responseID})
	w.enqueue(map[string]any{"type": "response.done", "response_id": responseID, "response": map[string]any{"id": responseID, "status": "completed"}})
	w.enqueue(map[string]any{"type": "session.closed", "session_id": "sess-s2s-interrupt", "reason": "fixture_complete"})
}

func (w *sessionAudioInterruptWire) fail(err error) {
	w.mu.Lock()
	w.failLocked(err)
	w.mu.Unlock()
	if err != nil {
		w.close.Do(func() { close(w.done) })
	}
}

func (w *sessionAudioInterruptWire) failLocked(err error) {
	if err != nil && w.protocolErr == nil {
		w.protocolErr = err
	}
}

func (w *sessionAudioInterruptWire) protocolError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.protocolErr
}

func (w *sessionAudioInterruptWire) writesSnapshot() []sessionAudioInterruptWireWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	writes := make([]sessionAudioInterruptWireWrite, len(w.writes))
	copy(writes, w.writes)
	return writes
}

func (w *sessionAudioInterruptWire) writeSummary() string {
	return sessionAudioInterruptWriteSummary(w.writesSnapshot())
}

func (w *sessionAudioInterruptWire) eventsSnapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...)
}

func sessionAudioInterruptWriteSummary(writes []sessionAudioInterruptWireWrite) string {
	types := make([]string, 0, len(writes))
	for _, write := range writes {
		types = append(types, write.Type)
	}
	return fmt.Sprintf("%v", types)
}
