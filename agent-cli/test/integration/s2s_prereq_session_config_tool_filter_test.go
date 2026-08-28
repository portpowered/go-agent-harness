package integration

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionConfigToolFilterThroughRealCLI proves the selected config is
// applied at the shipped CLI/session boundary. The session inferencer is the
// only replaced external port; the tool executor remains the production
// registry-backed implementation created by the composition root.
func TestSessionConfigToolFilterThroughRealCLI(t *testing.T) {
	toolInput := filepath.Join(t.TempDir(), "session-tool-input.txt")
	const toolInputContents = "isolated session config tool fixture"
	if err := os.WriteFile(toolInput, []byte(toolInputContents), 0o600); err != nil {
		t.Fatalf("write isolated tool input: %v", err)
	}

	cases := []struct {
		name             string
		configYAML       string
		calls            []sessionConfigToolCall
		wantSleep        bool
		wantReadFile     bool
		wantSleepSuccess bool
	}{
		{
			name: "empty tools list enables default sleep",
			configYAML: `
model:
  provider: openrouter
tools:
  list: []
`,
			calls: []sessionConfigToolCall{
				{ID: "prereq-default-sleep", Name: "sleep", Arguments: `{"duration":"0s"}`},
			},
			wantSleep:        true,
			wantReadFile:     true,
			wantSleepSuccess: true,
		},
		{
			name: "disabled sleep keeps omitted read file",
			configYAML: `
model:
  provider: openrouter
tools:
  list:
    - id: sleep
      enabled: false
`,
			calls: []sessionConfigToolCall{
				{ID: "prereq-disabled-sleep", Name: "sleep", Arguments: `{"duration":"0s"}`},
				{ID: "prereq-disabled-read", Name: "read_file", Arguments: fmt.Sprintf(`{"path":%q}`, toolInput)},
			},
			wantReadFile: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(tc.configYAML), 0o600); err != nil {
				t.Fatalf("write session config: %v", err)
			}

			sessionInferencer := newSessionConfigToolInferencer(tc.calls)
			var resultMu sync.Mutex
			var advertised map[string]bool
			var resultText strings.Builder
			results := make([]sessionConfigToolResult, 0, len(tc.calls))
			currentResult := make(map[string]string)
			agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
				wire.NewPortSwap(wire.PortSessionInferencer, sessionInferencer),
			)
			if err != nil {
				t.Fatalf("initialize composed CLI: %v", err)
			}
			agentCLI.SetSessionStreamObserver(func(msg messages.StreamMessage) {
				resultMu.Lock()
				defer resultMu.Unlock()
				switch value := msg.Value.(type) {
				case *messages.TextDeltaValue:
					if msg.Role == messages.RoleTool {
						currentResult[msg.ToolCallId] += value.Content
						resultText.WriteString(value.Content)
					}
				case *messages.TextEndValue:
					if msg.Role == messages.RoleTool {
						results = append(results, sessionConfigToolResult{
							ToolCallID: msg.ToolCallId,
							Content:    currentResult[msg.ToolCallId],
						})
						delete(currentResult, msg.ToolCallId)
					}
				}
			})

			root := agentCLI.Generate()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{
				"--config-dir", configDir,
				"session",
				"--replay", filepath.Join(configDir, "deterministic.session.json"),
				"--wait-for-close",
				"invoke", "scripted", "tool",
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := root.ExecuteContext(ctx); err != nil {
				t.Fatalf("execute real session CLI: %v", err)
			}

			observedCalls := sessionInferencer.callsObserved()
			advertised = sessionInferencer.advertisedToolNames()
			if len(observedCalls) != len(tc.calls) {
				t.Fatalf("observed tool calls = %#v, want %#v", observedCalls, tc.calls)
			}
			for i := range tc.calls {
				if observedCalls[i] != tc.calls[i] {
					t.Fatalf("tool call %d = %#v, want %#v", i, observedCalls[i], tc.calls[i])
				}
			}

			resultMu.Lock()
			defer resultMu.Unlock()
			if advertised["sleep"] != tc.wantSleep {
				t.Fatalf("sleep advertised = %v, want %v; advertised=%v", advertised["sleep"], tc.wantSleep, advertised)
			}
			if advertised["read_file"] != tc.wantReadFile {
				t.Fatalf("read_file advertised = %v, want %v; advertised=%v", advertised["read_file"], tc.wantReadFile, advertised)
			}
			if len(results) != len(tc.calls) {
				t.Fatalf("tool results = %#v, want %d results", results, len(tc.calls))
			}
			for i, wantCall := range tc.calls {
				if results[i].ToolCallID != wantCall.ID {
					t.Fatalf("tool result %d correlated ID = %q, want %q", i, results[i].ToolCallID, wantCall.ID)
				}
			}
			if tc.wantSleepSuccess {
				if len(results) != 1 || results[0].Content != "Slept for 0s (no-op)." {
					t.Fatalf("default sleep result = %#v, want one successful no-op result", results)
				}
			} else {
				if strings.Contains(resultText.String(), "Slept for 0s (no-op).") {
					t.Fatalf("disabled sleep unexpectedly produced a successful result: %q", resultText.String())
				}
				if len(results) != 2 || !strings.Contains(results[0].Content, `tool "sleep" failed`) {
					t.Fatalf("disabled sleep result = %#v, want a correlated failure", results)
				}
				if len(results) != 2 || results[1].Content != toolInputContents {
					t.Fatalf("disabled-row read_file result = %#v, want isolated file contents", results)
				}
			}
		})
	}
}

func TestSessionConfigToolFilterRejectsInvalidConfigBeforeConnect(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("tools: [\n"), 0o600); err != nil {
		t.Fatalf("write invalid session config: %v", err)
	}

	sessionInferencer := newSessionConfigToolInferencer(nil)
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionInferencer, sessionInferencer),
	)
	if err != nil {
		t.Fatalf("initialize composed CLI: %v", err)
	}
	root := agentCLI.Generate()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--replay", filepath.Join(configDir, "invalid.session.json"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "load session config") {
		t.Fatalf("invalid config error = %v, want clear session config-loading error", err)
	}
	if sessionInferencer.sess != nil {
		t.Fatal("session provider connected despite invalid selected config")
	}
}

type sessionConfigToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type sessionConfigToolResult struct {
	ToolCallID string
	Content    string
}

type sessionConfigToolInferencer struct {
	calls []sessionConfigToolCall
	sess  *sessionConfigToolSession
}

func newSessionConfigToolInferencer(calls []sessionConfigToolCall) *sessionConfigToolInferencer {
	return &sessionConfigToolInferencer{calls: append([]sessionConfigToolCall(nil), calls...)}
}

func (i *sessionConfigToolInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.sess = &sessionConfigToolSession{
		inferencer: i,
		recv:       messages.NewTypedBuffer[messages.StreamMessage](64),
		done:       make(chan struct{}),
	}
	i.sess.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("session-config-tool-filter", "deterministic"),
	})
	i.sess.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("session-config-tool-filter", "deterministic"),
	})
	return i.sess, nil
}

func (i *sessionConfigToolInferencer) callsObserved() []sessionConfigToolCall {
	if i.sess == nil {
		return nil
	}
	i.sess.mu.Lock()
	defer i.sess.mu.Unlock()
	return append([]sessionConfigToolCall(nil), i.sess.observedCalls...)
}

func (i *sessionConfigToolInferencer) advertisedToolNames() map[string]bool {
	if i.sess == nil {
		return nil
	}
	i.sess.mu.Lock()
	defer i.sess.mu.Unlock()
	return i.sess.advertisedToolNames()
}

func (i *sessionConfigToolInferencer) close() {
	if i.sess != nil {
		_ = i.sess.Close()
	}
}

type sessionConfigToolSession struct {
	inferencer    *sessionConfigToolInferencer
	recv          *messages.TypedBuffer[messages.StreamMessage]
	done          chan struct{}
	once          sync.Once
	mu            sync.Mutex
	nextCall      int
	acceptedCalls int
	observedCalls []sessionConfigToolCall
	advertised    map[string]bool
}

func (s *sessionConfigToolSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.done:
		return false
	default:
	}
	if value, ok := msg.Value.(*messages.SessionUpdateValue); ok && value != nil {
		s.mu.Lock()
		if s.advertised == nil {
			s.advertised = make(map[string]bool, len(value.Tools))
		}
		for _, tool := range value.Tools {
			s.advertised[tool.Name] = true
		}
		s.mu.Unlock()
		return true
	}
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		s.mu.Lock()
		s.acceptedCalls++
		s.mu.Unlock()
		return true
	}
	if msg.Type == messages.StreamTypeResponseCreate {
		s.mu.Lock()
		closeAfterAcceptance := s.acceptedCalls == len(s.inferencer.calls)
		s.mu.Unlock()
		if closeAfterAcceptance {
			s.emitContinuation()
			s.inferencer.close()
		}
		return true
	}

	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value == nil {
		return true
	}

	s.mu.Lock()
	if s.nextCall >= len(s.inferencer.calls) {
		s.mu.Unlock()
		return true
	}
	calls := append([]sessionConfigToolCall(nil), s.inferencer.calls[s.nextCall:]...)
	s.nextCall = len(s.inferencer.calls)
	s.observedCalls = append(s.observedCalls, calls...)
	s.mu.Unlock()

	deltas := []messages.StreamMessage{{
		Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue(),
	}}
	for _, call := range calls {
		deltas = append(deltas,
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(call.ID, call.Name)},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments)},
		)
	}
	deltas = append(deltas, messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	for _, delta := range deltas {
		if !s.recv.Write(context.Background(), delta) {
			return false
		}
	}
	return true
}

func (s *sessionConfigToolSession) emitContinuation() {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("session config tool continuation")},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !s.recv.Write(context.Background(), msg) {
			return
		}
	}
}

func (s *sessionConfigToolSession) advertisedToolNames() map[string]bool {
	advertised := make(map[string]bool, len(s.advertised))
	for name, present := range s.advertised {
		advertised[name] = present
	}
	return advertised
}

func (s *sessionConfigToolSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *sessionConfigToolSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionConfigToolSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
