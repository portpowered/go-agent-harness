package agentruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	wireSessionUpdate          = "session.update"
	wireConversationItemCreate = "conversation.item.create"
)

const (
	agentsInstructionsMarker = "AGENTS_INSTRUCTIONS_MARKER"
	fileInstructionsMarker   = "FILE_INSTRUCTIONS_MARKER"
	rawInstructionsMarker    = "RAW_INSTRUCTIONS_MARKER"
	userTurnMarker           = "USER_TURN_MARKER"
)

var (
	_ transport.Dialer = (*recordingRealtimeTestDialer)(nil)
	_ transport.Conn   = (*recordingRealtimeTestConn)(nil)
)

func assertSessionInstructionEvents(t *testing.T, inferencer *sessionInstructionsTestInferencer, wantInstructions string, wantConfigCount int) {
	t.Helper()
	events := inferencer.sentEvents()
	configCount := 0
	userCount := 0
	configIndex := -1
	userIndex := -1
	gotInstructions := ""
	gotUserText := ""
	for index, event := range events {
		switch {
		case event.Type == messages.StreamTypeSessionUpdate:
			configCount++
			configIndex = index
			value, ok := event.Value.(*messages.SessionUpdateValue)
			if !ok || value == nil {
				t.Fatalf("session update event has value %T, want *SessionUpdateValue", event.Value)
			}
			gotInstructions = value.Instructions
		case event.Type == messages.StreamTypeTextDelta:
			value, ok := event.Value.(*messages.TextDeltaValue)
			if !ok || value == nil {
				continue
			}
			userCount++
			userIndex = index
			gotUserText = value.Content
		}
	}
	if configCount != wantConfigCount {
		t.Fatalf("instruction-bearing session configuration count = %d, want %d; events=%s", configCount, wantConfigCount, formatSessionEvents(events))
	}
	if userCount != 1 {
		t.Fatalf("user-turn event count = %d, want 1; events=%s", userCount, formatSessionEvents(events))
	}
	if gotUserText != userTurnMarker {
		t.Fatalf("first user-turn text = %q, want %q", gotUserText, userTurnMarker)
	}
	if gotInstructions != wantInstructions {
		t.Fatalf("session instructions = %q, want %q", gotInstructions, wantInstructions)
	}
	if wantConfigCount > 0 && configIndex >= userIndex {
		t.Fatalf("session configuration index = %d, user-turn index = %d; events=%s", configIndex, userIndex, formatSessionEvents(events))
	}
}

func assertSessionInstructionEventsWithGrounding(t *testing.T, inferencer *sessionInstructionsTestInferencer, wantBase string, wantConfigCount int) {
	t.Helper()
	events := inferencer.sentEvents()
	configCount := 0
	userCount := 0
	gotInstructions := ""
	for _, event := range events {
		switch {
		case event.Type == messages.StreamTypeSessionUpdate:
			configCount++
			value, ok := event.Value.(*messages.SessionUpdateValue)
			if !ok || value == nil {
				t.Fatalf("session update event has value %T, want *SessionUpdateValue", event.Value)
			}
			gotInstructions = value.Instructions
		case event.Type == messages.StreamTypeTextDelta:
			value, ok := event.Value.(*messages.TextDeltaValue)
			if ok && value != nil {
				userCount++
			}
		}
	}
	if configCount != wantConfigCount {
		t.Fatalf("grounded session configuration count = %d, want %d; events=%s", configCount, wantConfigCount, formatSessionEvents(events))
	}
	if userCount != 1 {
		t.Fatalf("grounded user-turn event count = %d, want 1; events=%s", userCount, formatSessionEvents(events))
	}
	if !strings.HasPrefix(gotInstructions, wantBase+"\n\n") {
		t.Fatalf("grounded session instructions = %q, want base prefix %q", gotInstructions, wantBase+"\n\n")
	}
	if strings.Count(gotInstructions, "Tool-grounding requirements:") != 1 {
		t.Fatalf("grounding policy heading count = %d, want 1; instructions=%q", strings.Count(gotInstructions, "Tool-grounding requirements:"), gotInstructions)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func formatSessionEvents(events []messages.StreamMessage) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, string(event.Type))
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, ", "))
}

func longLiteralSystemPrompt() string {
	return strings.Repeat("Preserve this literal: spaces, punctuation !?; path-like fragments /tmp/not-a-file.md and ./missing-prompt.txt.\n", 16)
}

type recordingRealtimeTestDialer struct {
	mu   sync.Mutex
	conn *recordingRealtimeTestConn
	url  string
}

func (d *recordingRealtimeTestDialer) Dial(url string, _ map[string]string) (transport.Conn, error) {
	d.mu.Lock()
	d.url = url
	d.mu.Unlock()
	return d.conn, nil
}

func (d *recordingRealtimeTestDialer) dialedURL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.url
}

type recordingRealtimeTestConn struct {
	mu                        sync.Mutex
	writes                    [][]byte
	inbound                   chan []byte
	closed                    chan struct{}
	closeOnce                 sync.Once
	respondToConversationItem bool
}

func newRecordingRealtimeTestConn() *recordingRealtimeTestConn {
	c := &recordingRealtimeTestConn{
		inbound: make(chan []byte, 16),
		closed:  make(chan struct{}),
	}
	c.inbound <- []byte(`{"type":"session.created","session_id":"test-session","model":"gpt-realtime"}`)
	return c
}

func (c *recordingRealtimeTestConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.inbound:
		return 1, payload, nil
	case <-c.closed:
		return 0, nil, io.EOF
	}
}

func (c *recordingRealtimeTestConn) WriteMessage(_ int, payload []byte) error {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	c.mu.Unlock()

	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil && (envelope.Type == "response.create" || (c.respondToConversationItem && envelope.Type == wireConversationItemCreate)) {
		// A bare response.done is an empty provider response and is not a
		// successful session turn. Keep this test transport contentful so the
		// instruction assertions exercise the normal completion path.
		c.inbound <- []byte(`{"type":"response.created"}`)
		c.inbound <- []byte(`{"type":"response.output_audio_transcript.delta","delta":"ok"}`)
		c.inbound <- []byte(`{"type":"response.done"}`)
	}
	return nil
}

func (c *recordingRealtimeTestConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

type sessionInstructionsTestInferencer struct {
	mu                   sync.Mutex
	connected            bool
	rejectSessionUpdates bool
	session              *sessionInstructionsTestSession
}

func newSessionInstructionsTestInferencer() *sessionInstructionsTestInferencer {
	return &sessionInstructionsTestInferencer{}
}

func newSessionInstructionsTestInferencerRejectingUpdates() *sessionInstructionsTestInferencer {
	return &sessionInstructionsTestInferencer{rejectSessionUpdates: true}
}

func (i *sessionInstructionsTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.mu.Lock()
	i.connected = true
	i.session = newSessionInstructionsTestSession(i.rejectSessionUpdates)
	session := i.session
	i.mu.Unlock()
	go func() {
		session.receive.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("instructions-test-session", "test"),
		})
	}()
	return session, nil
}

func (i *sessionInstructionsTestInferencer) wasConnected() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connected
}

func (i *sessionInstructionsTestInferencer) sentEvents() []messages.StreamMessage {
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.sentEvents()
}

type sessionInstructionsTestSession struct {
	mu                   sync.Mutex
	receive              *messages.TypedBuffer[messages.StreamMessage]
	sent                 []messages.StreamMessage
	done                 chan struct{}
	closeOnce            sync.Once
	responseOnce         sync.Once
	rejectSessionUpdates bool
}

func newSessionInstructionsTestSession(rejectSessionUpdates bool) *sessionInstructionsTestSession {
	return &sessionInstructionsTestSession{
		receive:              messages.NewTypedBuffer[messages.StreamMessage](32),
		done:                 make(chan struct{}),
		rejectSessionUpdates: rejectSessionUpdates,
	}
}

func (s *sessionInstructionsTestSession) Send(ctx context.Context, event messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, event)
	rejectSessionUpdate := s.rejectSessionUpdates && event.Type == messages.StreamTypeSessionUpdate
	s.mu.Unlock()
	if rejectSessionUpdate {
		return false
	}
	if event.Type == messages.StreamTypeTextDelta {
		s.responseOnce.Do(func() {
			s.receive.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeTextEnd,
				Value: messages.NewTextEndValue(),
			})
		})
	}
	return true
}

func (s *sessionInstructionsTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionInstructionsTestSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionInstructionsTestSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
	})
	return nil
}

func (s *sessionInstructionsTestSession) sentEvents() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

type instructionSourceCase struct {
	name              string
	setup             func(t *testing.T, workspaceDir string) string
	want              func(t *testing.T, workspaceDir, explicit string) string
	wantConfigCount   int
	wantError         bool
	wantErrorContains string
	skipWorkspace     bool
}

func assertPromptResolutionError(t *testing.T, err error, wantContains string, inferencer *sessionInstructionsTestInferencer) {
	t.Helper()
	if err == nil {
		t.Fatal("expected prompt-resolution error")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("prompt-resolution error = %v, want wrapped *os.PathError", err)
	}
	if !strings.Contains(err.Error(), wantContains) {
		t.Fatalf("prompt-resolution error = %v, want %q", err, wantContains)
	}
	if inferencer.wasConnected() {
		t.Fatal("inaccessible workspace connected a session before returning its prompt error")
	}
}

func sessionScopeSuffix(t *testing.T, workspaceDir string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		t.Fatalf("resolve workspace symlinks: %v", err)
	}
	return "\n\nFilesystem scope: workdir=" + canonical + "; additional_allowed_roots=none. Relative filesystem-tool paths resolve from this workdir."
}

// openAIInitialTurn summarizes the configuration and first user events sent to
// the OpenAI Realtime wire.
type openAIInitialTurn struct {
	configIndex, userIndex, configCount, userCount int
	instructions, voice, userText                  string
}

type openAIOutboundEnvelope struct {
	Type    string `json:"type"`
	Session struct {
		Instructions string `json:"instructions"`
		Audio        struct {
			Output struct {
				Voice string `json:"voice"`
			} `json:"output"`
		} `json:"audio"`
	} `json:"session"`
	Item struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
}

func summarizeOpenAIInitialTurn(t *testing.T, records []gwtesting.CapturedSessionEvent) openAIInitialTurn {
	t.Helper()
	summary := openAIInitialTurn{configIndex: -1, userIndex: -1}
	for index, event := range records {
		if event.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		payload := event.Payload
		if len(payload) == 0 {
			payload = event.Data
		}
		var envelope openAIOutboundEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode outbound event %q: %v", string(payload), err)
		}
		switch envelope.Type {
		case wireSessionUpdate:
			summary.configCount++
			summary.configIndex = index
			summary.instructions = envelope.Session.Instructions
			summary.voice = envelope.Session.Audio.Output.Voice
		case wireConversationItemCreate:
			summary.userCount++
			summary.userIndex = index
			if len(envelope.Item.Content) == 1 {
				summary.userText = envelope.Item.Content[0].Text
			}
		}
	}
	return summary
}
