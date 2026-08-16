package services_test

import (
	"bytes"
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
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/workspace"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	agentsInstructionsMarker = "AGENTS_INSTRUCTIONS_MARKER"
	fileInstructionsMarker   = "FILE_INSTRUCTIONS_MARKER"
	rawInstructionsMarker    = "RAW_INSTRUCTIONS_MARKER"
	userTurnMarker           = "USER_TURN_MARKER"
)

func TestRunSessionWithInstructions_SourceMatrix(t *testing.T) {
	tests := []struct {
		name              string
		setup             func(t *testing.T, workspaceDir string) string
		want              func(t *testing.T, workspaceDir, explicit string) string
		wantConfigCount   int
		wantError         bool
		wantGeneratedFile bool
	}{
		{
			name: "absent AGENTS.md generates default instructions",
			setup: func(*testing.T, string) string {
				return ""
			},
			want: func(t *testing.T, workspaceDir, _ string) string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(workspaceDir, workspace.AgentsMDFileName))
				if err != nil {
					t.Fatalf("read generated AGENTS.md: %v", err)
				}
				return string(data)
			},
			wantConfigCount:   1,
			wantGeneratedFile: true,
		},
		{
			name: "AGENTS.md content",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				return ""
			},
			want: func(_ *testing.T, _, _ string) string {
				return agentsInstructionsMarker
			},
			wantConfigCount: 1,
		},
		{
			name: "empty AGENTS.md keeps session running without instructions",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), "")
				return ""
			},
			want: func(_ *testing.T, _, _ string) string {
				return ""
			},
			wantConfigCount: 0,
		},
		{
			name: "unreadable AGENTS.md keeps session running without instructions",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				if err := os.Mkdir(filepath.Join(workspaceDir, workspace.AgentsMDFileName), 0755); err != nil {
					t.Fatalf("make unreadable AGENTS.md entry: %v", err)
				}
				return ""
			},
			want: func(_ *testing.T, _, _ string) string {
				return ""
			},
			wantConfigCount: 0,
		},
		{
			name: "inaccessible workspace fails before session configuration",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
					t.Fatalf("inaccessible workspace unexpectedly exists: %v", err)
				}
				return ""
			},
			wantError: true,
		},
		{
			name: "explicit prompt file wins over AGENTS.md",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				promptPath := filepath.Join(workspaceDir, "prompt.md")
				writeFile(t, promptPath, fileInstructionsMarker)
				return promptPath
			},
			want: func(_ *testing.T, _, _ string) string {
				return fileInstructionsMarker
			},
			wantConfigCount: 1,
		},
		{
			name: "explicit raw text wins over AGENTS.md",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				return rawInstructionsMarker
			},
			want: func(_ *testing.T, _, _ string) string {
				return rawInstructionsMarker
			},
			wantConfigCount: 1,
		},
		{
			name: "missing prompt path is literal text",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				return filepath.Join(workspaceDir, "missing-prompt.md")
			},
			want: func(_ *testing.T, _, explicit string) string {
				return explicit
			},
			wantConfigCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceDir := filepath.Join(t.TempDir(), "workspace")
			if !tt.wantError {
				if err := os.Mkdir(workspaceDir, 0755); err != nil {
					t.Fatalf("create workspace: %v", err)
				}
			}
			explicit := tt.setup(t, workspaceDir)
			inferencer := newSessionInstructionsTestInferencer()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			err := services.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), services.SessionRunOptions{
				ReplayPath:        filepath.Join(workspaceDir, "session.json"),
				ConfigDir:         workspaceDir,
				Prompt:            userTurnMarker,
				SessionInferencer: inferencer,
			}, explicit)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected prompt-resolution error")
				}
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatalf("prompt-resolution error = %v, want wrapped *os.PathError", err)
				}
				if !strings.Contains(err.Error(), "initialize AGENTS.md") {
					t.Fatalf("prompt-resolution error = %v, want AGENTS.md initialization context", err)
				}
				if inferencer.wasConnected() {
					t.Fatal("inaccessible workspace connected a session before returning its prompt error")
				}
				return
			}
			if err != nil {
				t.Fatalf("RunSessionWithInstructions: %v", err)
			}

			if tt.wantGeneratedFile {
				if _, err := os.Stat(filepath.Join(workspaceDir, workspace.AgentsMDFileName)); err != nil {
					t.Fatalf("default resolution did not create AGENTS.md: %v", err)
				}
			}
			wantInstructions := tt.want(t, workspaceDir, explicit)
			assertSessionInstructionEvents(t, inferencer, wantInstructions, tt.wantConfigCount)
		})
	}
}

func TestSessionCommand_SystemPromptFlagForwardsLiteralAndPrecedesUserTurn(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	inferencer := newSessionInstructionsTestInferencer()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = workspaceDir
	askFlags := flags.NewAskFlags()
	cmd := cli.NewSessionCommand(askFlags, globalFlags, inferencer).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"--replay", filepath.Join(workspaceDir, "session.json"),
		"--system-prompt", rawInstructionsMarker,
		userTurnMarker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("session command with --system-prompt: %v", err)
	}
	flag := cmd.Flags().Lookup("system-prompt")
	if flag == nil {
		t.Fatal("session command did not expose --system-prompt")
	}
	if !strings.Contains(flag.Usage, "literal text") {
		t.Fatalf("--system-prompt help = %q, want path and literal-text contract", flag.Usage)
	}
	assertSessionInstructionEvents(t, inferencer, rawInstructionsMarker, 1)
}

func TestRunSessionWithInstructions_OpenAIInitialConfigPrecedesUserTurn(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	writeFile(t, filepath.Join(workspaceDir, config.ConfigFileName), "model:\n  provider: openai\n")
	realtimeConn := newRecordingRealtimeTestConn()
	realtimeDialer := &recordingRealtimeTestDialer{conn: realtimeConn}
	recordPath := filepath.Join(t.TempDir(), "openai-session.json")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructions(ctx, io.Discard, services.SessionRunOptions{
		RecordPath:      recordPath,
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-realtime",
		APIKey:          "test-api-key",
		ConfigDir:       workspaceDir,
		Prompt:          userTurnMarker,
		WebSocketDialer: realtimeDialer,
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("LoadSessionCapture: %v", err)
	}
	configIndex := -1
	userIndex := -1
	configCount := 0
	userCount := 0
	gotInstructions := ""
	gotUserText := ""
	for index, event := range capture.Records {
		if event.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		payload := event.Payload
		if len(payload) == 0 {
			payload = event.Data
		}
		var envelope struct {
			Type    string `json:"type"`
			Session struct {
				Instructions string `json:"instructions"`
			} `json:"session"`
			Item struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode outbound event %q: %v", string(payload), err)
		}
		switch envelope.Type {
		case "session.update":
			configCount++
			configIndex = index
			gotInstructions = envelope.Session.Instructions
		case "conversation.item.create":
			userCount++
			userIndex = index
			if len(envelope.Item.Content) == 1 {
				gotUserText = envelope.Item.Content[0].Text
			}
		}
	}
	if configCount != 1 {
		t.Fatalf("OpenAI instruction-bearing session.update count = %d, want 1; capture=%#v", configCount, capture.Records)
	}
	if gotInstructions != agentsInstructionsMarker {
		t.Fatalf("OpenAI initial session instructions = %q, want %q", gotInstructions, agentsInstructionsMarker)
	}
	if userCount != 1 || gotUserText != userTurnMarker {
		t.Fatalf("OpenAI first user event = count %d text %q, want count 1 text %q", userCount, gotUserText, userTurnMarker)
	}
	if strings.Contains(gotUserText, agentsInstructionsMarker) {
		t.Fatalf("OpenAI first user event duplicated session instructions: %q", gotUserText)
	}
	if configIndex < 0 || userIndex < 0 || configIndex >= userIndex {
		t.Fatalf("OpenAI session.update index = %d, first user index = %d; capture=%#v", configIndex, userIndex, capture.Records)
	}
}

func TestRunSessionWithInstructions_ConfigurationSendFailureStopsBeforeUserTurn(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	inferencer := newSessionInstructionsTestInferencerRejectingUpdates()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructions(ctx, &out, services.SessionRunOptions{
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		Prompt:            userTurnMarker,
		SessionInferencer: inferencer,
	}, "")
	if err == nil {
		t.Fatal("expected session configuration send failure")
	}
	if !strings.Contains(err.Error(), "send session instructions") {
		t.Fatalf("session configuration send error = %v, want typed send context", err)
	}
	for _, event := range inferencer.sentEvents() {
		if event.Type == messages.StreamTypeTextDelta {
			t.Fatal("user turn was sent after session configuration failed")
		}
	}
}

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
		switch event.Type {
		case messages.StreamTypeSessionUpdate:
			configCount++
			configIndex = index
			value, ok := event.Value.(*messages.SessionUpdateValue)
			if !ok || value == nil {
				t.Fatalf("session update event has value %T, want *SessionUpdateValue", event.Value)
			}
			gotInstructions = value.Instructions
		case messages.StreamTypeTextDelta:
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func formatSessionEvents(events []messages.StreamMessage) string {
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, string(event.Type))
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, ", "))
}

type recordingRealtimeTestDialer struct {
	conn *recordingRealtimeTestConn
}

var _ grok.WebSocketDialer = (*recordingRealtimeTestDialer)(nil)

func (d *recordingRealtimeTestDialer) Dial(string, map[string]string) (grok.WebSocketConn, error) {
	return d.conn, nil
}

type recordingRealtimeTestConn struct {
	mu        sync.Mutex
	writes    [][]byte
	inbound   chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

var _ grok.WebSocketConn = (*recordingRealtimeTestConn)(nil)

func newRecordingRealtimeTestConn() *recordingRealtimeTestConn {
	c := &recordingRealtimeTestConn{
		inbound: make(chan []byte, 4),
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
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Type == "response.create" {
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
