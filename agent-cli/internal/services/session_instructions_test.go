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
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
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
				if !strings.Contains(err.Error(), "invalid filesystem root") {
					t.Fatalf("prompt-resolution error = %v, want filesystem-scope validation context", err)
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

func TestRunSessionWithInstructions_DefaultAgentsMDUsesEffectiveToolDefinitions(t *testing.T) {
	workspaceDir := t.TempDir()
	inferencer := newSessionInstructionsTestInferencer()
	toolDefinitions := []messages.ToolDefinition{
		{Name: "read_file", Description: "Read a UTF-8 file from the workspace."},
		{Name: "exec", Description: "Execute a command in the workspace."},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), services.SessionRunOptions{
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		Prompt:            userTurnMarker,
		SessionInferencer: inferencer,
		ToolDefinitions:   toolDefinitions,
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(workspaceDir, workspace.AgentsMDFileName))
	if err != nil {
		t.Fatalf("read generated AGENTS.md: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "### `read_file`") || !strings.Contains(got, "### `exec`") {
		t.Fatalf("generated AGENTS.md does not reflect session tools: %s", got)
	}
	if strings.Contains(got, "No tools are currently registered.") {
		t.Fatalf("generated AGENTS.md contradicts session tools: %s", got)
	}
	assertSessionInstructionEventsWithGrounding(t, inferencer, got, 1)
}

func TestRunSessionWithInstructions_ExplicitPromptDoesNotReconcileAgentsMD(t *testing.T) {
	workspaceDir := t.TempDir()
	staleAgents := "customer instructions\n\n## Available Tools\n\nNo tools are currently registered.\n## Notes\nkeep this section\n"
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), staleAgents)
	promptPath := filepath.Join(workspaceDir, "prompt.md")
	writeFile(t, promptPath, fileInstructionsMarker)
	inferencer := newSessionInstructionsTestInferencer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), services.SessionRunOptions{
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		Prompt:            userTurnMarker,
		SessionInferencer: inferencer,
		ToolDefinitions: []messages.ToolDefinition{{
			Name:        "read_file",
			Description: "Read a UTF-8 file from the workspace.",
		}},
	}, promptPath)
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}
	if got := string(mustReadFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName))); got != staleAgents {
		t.Fatalf("explicit prompt resolution changed AGENTS.md:\n got: %q\nwant: %q", got, staleAgents)
	}
	assertSessionInstructionEventsWithGrounding(t, inferencer, fileInstructionsMarker, 1)
}

func TestRunSessionWithInstructions_OpenAIInitialConfigCarriesGroundingWithTools(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	writeFile(t, filepath.Join(workspaceDir, config.ConfigFileName), "model:\n  provider: openai\n")
	realtimeConn := newRecordingRealtimeTestConn()
	recordPath := filepath.Join(t.TempDir(), "openai-grounding.session.json")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructions(ctx, io.Discard, services.SessionRunOptions{
		RecordPath:      recordPath,
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-realtime",
		APIKey:          "test-api-key",
		ConfigDir:       workspaceDir,
		Prompt:          userTurnMarker,
		ToolExecutor:    &messages.DefaultToolExecutor{},
		ToolDefinitions: []messages.ToolDefinition{{Name: "inspect_machine", Description: "Inspect machine state"}},
		WebSocketDialer: &recordingRealtimeTestDialer{conn: realtimeConn},
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("LoadSessionCapture: %v", err)
	}
	configCount := 0
	configIndex := -1
	userIndex := -1
	gotInstructions := ""
	toolCount := 0
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
				Instructions string            `json:"instructions"`
				Tools        []json.RawMessage `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode outbound event %q: %v", string(payload), err)
		}
		switch envelope.Type {
		case "session.update":
			configCount++
			configIndex = index
			gotInstructions = envelope.Session.Instructions
			toolCount = len(envelope.Session.Tools)
		case "conversation.item.create":
			userIndex = index
		}
	}
	if configCount != 1 {
		t.Fatalf("instruction-bearing OpenAI session.update count = %d, want 1; capture=%#v", configCount, capture.Records)
	}
	if !strings.HasPrefix(gotInstructions, agentsInstructionsMarker+"\n\n") {
		t.Fatalf("grounding instructions = %q, want workspace instructions first", gotInstructions)
	}
	if strings.Count(gotInstructions, "Tool-grounding requirements:") != 1 {
		t.Fatalf("grounding policy heading count = %d, want 1; instructions=%q", strings.Count(gotInstructions, "Tool-grounding requirements:"), gotInstructions)
	}
	if strings.Contains(gotInstructions, "No tools are currently registered") {
		t.Fatalf("grounding instructions contradict advertised tools: %q", gotInstructions)
	}
	if toolCount != 1 {
		t.Fatalf("OpenAI session.update tools = %d, want 1", toolCount)
	}
	if configIndex < 0 || userIndex < 0 || configIndex >= userIndex {
		t.Fatalf("OpenAI session.update index = %d, first user index = %d; capture=%#v", configIndex, userIndex, capture.Records)
	}
}

func TestSessionCommand_SystemPromptFlagForwardsLiteralAndPrecedesUserTurn(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	inferencer := newSessionInstructionsTestInferencer()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = workspaceDir
	globalFlags.WorkDirPath = workspaceDir
	askFlags := flags.NewAskFlags()
	cmd := cli.NewSessionCommand(askFlags, globalFlags, nil, inferencer).Generate()
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
	assertSessionInstructionEvents(t, inferencer, rawInstructionsMarker+sessionScopeSuffix(t, workspaceDir), 1)
}

func TestSessionCommand_SystemPromptFlagForwardsLongLiteralUnchanged(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	longPrompt := longLiteralSystemPrompt()
	if len(longPrompt) < 1024 || len(longPrompt) > 2048 {
		t.Fatalf("long prompt length = %d, want 1-2 KB", len(longPrompt))
	}
	inferencer := newSessionInstructionsTestInferencer()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = workspaceDir
	globalFlags.WorkDirPath = workspaceDir
	askFlags := flags.NewAskFlags()
	cmd := cli.NewSessionCommand(askFlags, globalFlags, nil, inferencer).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"--replay", filepath.Join(workspaceDir, "session.json"),
		"--system-prompt", longPrompt,
		userTurnMarker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("session command with long --system-prompt: %v", err)
	}
	assertSessionInstructionEvents(t, inferencer, longPrompt+sessionScopeSuffix(t, workspaceDir), 1)
}

func TestSessionCommand_DisclosesOneEffectiveFilesystemScope(t *testing.T) {
	workspaceDir := t.TempDir()
	configDir := t.TempDir()
	inferencer := newSessionInstructionsTestInferencer()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	globalFlags.WorkDirPath = workspaceDir
	askFlags := flags.NewAskFlags()
	cmd := cli.NewSessionCommand(askFlags, globalFlags, nil, inferencer).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"--replay", filepath.Join(configDir, "session.json"),
		"--system-prompt", rawInstructionsMarker,
		userTurnMarker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("session command: %v", err)
	}

	canonical, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		t.Fatalf("resolve effective workdir: %v", err)
	}
	scope := "Filesystem scope: workdir=" + canonical + "; additional_allowed_roots=none"
	if !strings.Contains(out.String(), scope) {
		t.Fatalf("startup output = %q, want effective scope %q", out.String(), scope)
	}
	events := inferencer.sentEvents()
	var instructions string
	for _, event := range events {
		if event.Type != messages.StreamTypeSessionUpdate {
			continue
		}
		value, ok := event.Value.(*messages.SessionUpdateValue)
		if ok && value != nil {
			instructions = value.Instructions
		}
	}
	if !strings.Contains(instructions, scope) {
		t.Fatalf("session instructions = %q, want effective scope %q", instructions, scope)
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

func TestRunSessionWithInstructionsAndOptions_PreservesExplicitSeed(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	inferencer := newSessionInstructionsTestInferencer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, io.Discard, services.SessionRunOptions{
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		SessionInferencer: inferencer,
	}, "", 0, services.SessionTextSeed{Value: userTurnMarker, Present: true}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration: %v", err)
	}
	assertSessionInstructionEvents(t, inferencer, agentsInstructionsMarker, 1)
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
		Voice:           "marin",
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
	gotVoice := ""
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
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode outbound event %q: %v", string(payload), err)
		}
		switch envelope.Type {
		case "session.update":
			configCount++
			configIndex = index
			gotInstructions = envelope.Session.Instructions
			gotVoice = envelope.Session.Audio.Output.Voice
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
	if gotVoice != "marin" {
		t.Fatalf("OpenAI initial session voice = %q, want marin", gotVoice)
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

func TestRunSessionWithInstructions_GrokWhitespaceBaseURLUsesDefaultEndpoint(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	writeFile(t, filepath.Join(workspaceDir, config.ConfigFileName), `
model:
  provider: grok
  grok:
    model: grok-3-mini
    api_key: file-api-key
    base_url: "   "
`)
	conn := newRecordingRealtimeTestConn()
	conn.respondToConversationItem = true
	dialer := &recordingRealtimeTestDialer{conn: conn}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := services.RunSessionWithInstructions(ctx, io.Discard, services.SessionRunOptions{
		RecordPath:      filepath.Join(t.TempDir(), "grok-session.json"),
		Provider:        config.ProviderGrok,
		Model:           "grok-3-mini",
		APIKey:          "test-api-key",
		ConfigDir:       workspaceDir,
		Prompt:          userTurnMarker,
		WebSocketDialer: dialer,
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	if got := dialer.dialedURL(); got != "https://api.x.ai/v1/realtime" {
		t.Fatalf("Grok dial URL with whitespace-only BaseURL = %q, want default endpoint", got)
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

func assertSessionInstructionEventsWithGrounding(t *testing.T, inferencer *sessionInstructionsTestInferencer, wantBase string, wantConfigCount int) {
	t.Helper()
	events := inferencer.sentEvents()
	configCount := 0
	userCount := 0
	gotInstructions := ""
	for _, event := range events {
		switch event.Type {
		case messages.StreamTypeSessionUpdate:
			configCount++
			value, ok := event.Value.(*messages.SessionUpdateValue)
			if !ok || value == nil {
				t.Fatalf("session update event has value %T, want *SessionUpdateValue", event.Value)
			}
			gotInstructions = value.Instructions
		case messages.StreamTypeTextDelta:
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

var _ transport.Dialer = (*recordingRealtimeTestDialer)(nil)

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

var _ transport.Conn = (*recordingRealtimeTestConn)(nil)

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
	if err := json.Unmarshal(payload, &envelope); err == nil && (envelope.Type == "response.create" || (c.respondToConversationItem && envelope.Type == "conversation.item.create")) {
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
