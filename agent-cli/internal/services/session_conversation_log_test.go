package services

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestSessionConversationCollectorRecordsOrderedTurns(t *testing.T) {
	collector := &sessionConversationCollector{}
	outbound := func(msg messages.StreamMessage, inputIndex int) {
		collector.observe(msg, true, inputIndex, -1)
	}
	inbound := func(msg messages.StreamMessage, outputIndex int) {
		collector.observe(msg, false, -1, outputIndex)
	}

	outbound(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4})}, 0)
	outbound(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{5, 6})}, 1)
	outbound(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, -1)
	inbound(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("ZEPHYR noted.")}, -1)
	inbound(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(" I will remember it.")}, -1)
	inbound(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00, 0x11, 0x00})}, 0)
	inbound(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, -1)

	log, err := sessionConversationLogJSON(collector)
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"\",\"audio_bytes\":6,\"committed\":true,\"audio_segments\":[\"audio/in-000.pcm\",\"audio/in-001.pcm\"]}," +
		"\"response\":{\"text\":\"ZEPHYR noted. I will remember it.\",\"complete\":true,\"audio_bytes\":4,\"audio_segments\":[\"audio/out-000.pcm\"]}}\n"
	if string(log) != want {
		t.Fatalf("session log = %q, want %q", log, want)
	}
}

func TestSessionConversationCollectorRecordsCorrelatedToolLifecycleInSpokenTurn(t *testing.T) {
	collector := &sessionConversationCollector{}
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleUser,
		Value: messages.NewTextDeltaValue("check the browser"),
	}, true, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleUser,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}, true, -1, -1)

	first := messages.ToolCall{ID: "call-1", Name: "inspect_tab", Arguments: `{"tab":"first"}`}
	second := messages.ToolCall{ID: "call-2", Name: "inspect_tab", Arguments: `{"tab":"second"}`}
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallStartValue(first.ID, first.Name),
	}, false, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}, false, -1, -1)
	collector.observeToolCall(first)
	collector.observeToolResult(first, messages.ToolCallResponse{
		ToolCallID: first.ID,
		Name:       first.Name,
		Content:    `{"version":"webmcp.tool-result.v1","ok":true,"data":{"title":"first"}}`,
	}, false)
	collector.observeToolCall(second)
	collector.observeToolResult(second, messages.ToolCallResponse{
		ToolCallID: second.ID,
		Name:       second.Name,
		Content:    `tool "inspect_tab" failed: unavailable`,
	}, true)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("the tab is ready"),
	}, false, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}, false, -1, -1)

	log, err := sessionConversationLogJSON(collector)
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"check the browser\",\"audio_bytes\":0,\"committed\":true},\"response\":{\"text\":\"the tab is ready\",\"complete\":true,\"audio_bytes\":0},\"tool_events\":[" +
		"{\"sequence\":1,\"type\":\"tool_call\",\"tool_call_id\":\"call-1\",\"tool_name\":\"inspect_tab\",\"arguments\":\"{\\\"tab\\\":\\\"first\\\"}\"}," +
		"{\"sequence\":2,\"type\":\"tool_result\",\"tool_call_id\":\"call-1\",\"tool_name\":\"inspect_tab\",\"status\":\"completed\",\"content\":\"{\\\"version\\\":\\\"webmcp.tool-result.v1\\\",\\\"ok\\\":true,\\\"data\\\":{\\\"title\\\":\\\"first\\\"}}\"}," +
		"{\"sequence\":3,\"type\":\"tool_call\",\"tool_call_id\":\"call-2\",\"tool_name\":\"inspect_tab\",\"arguments\":\"{\\\"tab\\\":\\\"second\\\"}\"}," +
		"{\"sequence\":4,\"type\":\"tool_result\",\"tool_call_id\":\"call-2\",\"tool_name\":\"inspect_tab\",\"status\":\"failed\",\"content\":\"tool \\\"inspect_tab\\\" failed: unavailable\"}]" +
		"}\n"
	if string(log) != want {
		t.Fatalf("session log = %q, want %q", log, want)
	}
}

func TestSessionConversationCollectorRecordsUserAudioTranscriptSeparately(t *testing.T) {
	collector := &sessionConversationCollector{}
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleUser,
		Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4}),
	}, true, 0, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleUser,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}, true, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptDelta,
		Role:  messages.RoleUser,
		Value: messages.NewTranscriptDeltaValue("hello "),
	}, false, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTranscriptEnd,
		Role:  messages.RoleUser,
		Value: messages.NewTranscriptEndValue("hello world"),
	}, false, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("reply"),
	}, false, -1, -1)
	collector.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}, false, -1, -1)

	log, err := sessionConversationLogJSON(collector)
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"hello world\",\"audio_bytes\":4,\"committed\":true,\"audio_segments\":[\"audio/in-000.pcm\"]},\"response\":{\"text\":\"reply\",\"complete\":true,\"audio_bytes\":0}}\n"
	if string(log) != want {
		t.Fatalf("session log = %q, want %q", log, want)
	}
}

func TestSessionConversationCollectorPrefersFullTranscriptAndFlagsPartialTurn(t *testing.T) {
	collector := &sessionConversationCollector{}
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("interim")}, false, -1, -1)
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue("Hello there.")}, false, -1, -1)
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, false, -1, -1)

	// The session ends mid-turn: observed audio but no assistant MESSAGE.END.
	collector.observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial reply")}, false, -1, -1)

	log, err := sessionConversationLogJSON(collector)
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	want := "{\"turn_index\":1,\"input\":{\"text\":\"\",\"audio_bytes\":0,\"committed\":false},\"response\":{\"text\":\"Hello there.\",\"complete\":true,\"audio_bytes\":0}}\n" +
		"{\"turn_index\":2,\"input\":{\"text\":\"\",\"audio_bytes\":0,\"committed\":false},\"response\":{\"text\":\"partial reply\",\"complete\":false,\"audio_bytes\":0}}\n"
	if string(log) != want {
		t.Fatalf("session log = %q, want %q", log, want)
	}
}

func TestSessionConversationLogJSONEmptyWhenNoTurnObserved(t *testing.T) {
	log, err := sessionConversationLogJSON(&sessionConversationCollector{})
	if err != nil {
		t.Fatalf("render session log: %v", err)
	}
	if log != nil {
		t.Fatalf("session log = %q, want nil for a conversation-free recording", log)
	}
}

func TestSessionDirectoryRecordingWritesConversationSessionLog(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture")
	plan := sessionRuntimePlan{provider: sessionProviderOpenAI}
	recording := newSessionDirectoryRecording(destination, plan, SessionRunOptions{Model: "gpt-realtime"})
	inner := newSessionRecordingTestSession()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapper := newSessionDirectoryRecordingSession(ctx, inner, recording)

	if !wrapper.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleUser,
		Value: messages.NewAudioDeltaValue([]byte{0x01, 0x00, 0x02, 0x00}),
	}) {
		t.Fatal("recording wrapper rejected input audio")
	}
	if !wrapper.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleUser,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}) {
		t.Fatal("recording wrapper rejected end-of-turn")
	}
	replies := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("ZEPHYR noted.")},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(" I will remember it.")},
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00})},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}
	for _, msg := range replies {
		if !inner.receive.Write(ctx, msg) {
			t.Fatal("test session rejected output message")
		}
	}
	for range replies {
		if _, ok := wrapper.Receive().ReadBlocking(ctx.Done()); !ok {
			t.Fatal("recording wrapper did not forward output message")
		}
	}
	if err := wrapper.Close(); err != nil {
		t.Fatalf("close recording wrapper: %v", err)
	}
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	sessionLogBytes, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read session-log.jsonl: %v", err)
	}
	wantLine := "{\"turn_index\":1,\"input\":{\"text\":\"\",\"audio_bytes\":4,\"committed\":true,\"audio_segments\":[\"audio/in-000.pcm\"]}," +
		"\"response\":{\"text\":\"ZEPHYR noted. I will remember it.\",\"complete\":true,\"audio_bytes\":2,\"audio_segments\":[\"audio/out-000.pcm\"]}}\n"
	if string(sessionLogBytes) != wantLine {
		t.Fatalf("session-log.jsonl = %q, want %q", sessionLogBytes, wantLine)
	}

	var manifest transcript.RecordingManifest
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	listed := false
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == "session-log.jsonl" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("manifest artifacts do not list session-log.jsonl")
	}
	if !bytes.Contains(manifestBytes, []byte("session-log.jsonl")) {
		t.Fatal("manifest.json does not mention session-log.jsonl")
	}
}

// spokenToolLifecycleInferencer waits for the session's spoken text turn before
// producing two provider tool requests, then closes immediately after their
// results. The missing final assistant turn deliberately proves that recording
// retains an incomplete turn's lifecycle evidence.
type spokenToolLifecycleInferencer struct {
	first  []messages.StreamMessage
	second []messages.StreamMessage
}

func (i *spokenToolLifecycleInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newRoundTripSession()
	go func() {
		write := func(msg messages.StreamMessage) bool {
			return session.recv.Write(ctx, msg)
		}
		if !write(messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("spoken-session", "session"),
		}) {
			return
		}
		// Text prompts are sent at the shared session boundary as a text delta;
		// there is no synthetic response.create for this user-side input.
		if !session.waitForSent(ctx, messages.StreamTypeTextDelta) {
			return
		}
		for _, event := range i.first {
			if !write(event) {
				return
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}
		for _, event := range i.second {
			if !write(event) {
				return
			}
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}
		write(messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("spoken-session", "stopped after tool results"),
		})
	}()
	return session, nil
}

var _ messages.SessionInferencer = (*spokenToolLifecycleInferencer)(nil)

func TestSessionDirectoryRecordingCapturesCorrelatedToolLifecycle(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "capture")
	secret := "session-recording-secret"
	firstResult := `{"version":"webmcp.tool-result.v1","ok":true,"data":{"title":"first"}}`
	secondResult := `{"version":"webmcp.tool-result.v1","ok":false,"error":{"code":"TAB_UNAVAILABLE","message":"` + secret + `"}}`
	out := newSignalingBuffer()
	inferencer := &spokenToolLifecycleInferencer{
		first:  toolCallEvents("call-1", "inspect_tab", `{"tab":"first"}`),
		second: toolCallEvents("call-2", "inspect_tab", `{"tab":"second"}`),
	}
	recording := newSessionDirectoryRecording(destination, sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{
		Model:  "gpt-realtime",
		APIKey: secret,
	})

	var mu sync.Mutex
	var calls []messages.ToolCall
	executor := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		index := len(calls)
		calls = append(calls, call)
		mu.Unlock()
		if index == 0 {
			return messages.ToolCallResponse{Content: firstResult}, nil
		}
		return messages.ToolCallResponse{Content: secondResult}, nil
	})

	err := runAgentLoopSession(context.Background(), out, &sessionDirectoryRecordingInferencer{
		inner:     inferencer,
		recording: recording,
	}, sessionLoopOptions{
		Prompt:                "check the browser",
		MaxDuration:           2 * time.Second,
		WaitForClose:          true,
		ToolExecutor:          executor,
		toolLifecycleObserver: recording,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	mu.Lock()
	if len(calls) != 2 {
		t.Fatalf("tool calls = %d, want 2: %#v\noutput:\n%s", len(calls), calls, out.String())
	}
	mu.Unlock()
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read session-log.jsonl: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("session log leaked recording credential: %s", data)
	}
	var entry struct {
		Input struct {
			Text string `json:"text"`
		} `json:"input"`
		Response struct {
			Text     string `json:"text"`
			Complete bool   `json:"complete"`
		} `json:"response"`
		ToolEvents []struct {
			Sequence   uint64 `json:"sequence"`
			Type       string `json:"type"`
			ToolCallID string `json:"tool_call_id"`
			ToolName   string `json:"tool_name"`
			Arguments  string `json:"arguments"`
			Status     string `json:"status"`
			Content    string `json:"content"`
		} `json:"tool_events"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("decode session-log.jsonl: %v\n%s", err, data)
	}
	if entry.Input.Text != "check the browser" || entry.Response.Text != "" || entry.Response.Complete {
		t.Fatalf("conversation summary = response=%q complete=%t", entry.Response.Text, entry.Response.Complete)
	}
	if len(entry.ToolEvents) != 4 {
		t.Fatalf("tool events = %d, want four call/result events: %#v", len(entry.ToolEvents), entry.ToolEvents)
	}
	wantEvents := []struct {
		sequence uint64
		typ      string
		id       string
		args     string
		status   string
		content  string
	}{
		{1, "tool_call", "call-1", `{"tab":"first"}`, "", ""},
		{2, "tool_result", "call-1", "", "completed", firstResult},
		{3, "tool_call", "call-2", `{"tab":"second"}`, "", ""},
		{4, "tool_result", "call-2", "", "failed", ""},
	}
	for index, want := range wantEvents {
		got := entry.ToolEvents[index]
		if got.Sequence != want.sequence || got.Type != want.typ || got.ToolCallID != want.id || got.ToolName != "inspect_tab" || got.Arguments != want.args || got.Status != want.status {
			t.Fatalf("tool event %d = %#v, want sequence=%d type=%s id=%s args=%q status=%s", index, got, want.sequence, want.typ, want.id, want.args, want.status)
		}
		if want.content != "" && got.Content != want.content {
			t.Fatalf("tool event %d content = %q, want exact envelope string %q", index, got.Content, want.content)
		}
	}
	if !strings.Contains(entry.ToolEvents[3].Content, "REDACTED") {
		t.Fatalf("failed result content = %q, want credential redaction marker", entry.ToolEvents[3].Content)
	}
}
