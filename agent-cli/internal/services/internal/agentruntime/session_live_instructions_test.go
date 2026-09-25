package agentruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/workspace"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func instructionSourceCases() []instructionSourceCase {
	return []instructionSourceCase{
		{
			name:  "absent AGENTS.md sends no instructions and creates no file",
			setup: func(*testing.T, string) string { return "" },
			want: func(t *testing.T, workspaceDir, _ string) string {
				t.Helper()
				if _, err := os.Stat(filepath.Join(workspaceDir, workspace.AgentsMDFileName)); !os.IsNotExist(err) {
					t.Fatalf("absent AGENTS.md gained a side effect: %v", err)
				}
				return ""
			},
			wantConfigCount: 0,
		},
		{
			name: "AGENTS.md content",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				return ""
			},
			want:            func(_ *testing.T, _, _ string) string { return agentsInstructionsMarker },
			wantConfigCount: 1,
		},
		{
			name: "empty AGENTS.md keeps session running without instructions",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), "")
				return ""
			},
			want:            func(_ *testing.T, _, _ string) string { return "" },
			wantConfigCount: 0,
		},
		{
			name: "unreadable AGENTS.md returns an actionable error",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				if err := os.Mkdir(filepath.Join(workspaceDir, workspace.AgentsMDFileName), 0755); err != nil {
					t.Fatalf("make unreadable AGENTS.md entry: %v", err)
				}
				return ""
			},
			wantError:         true,
			wantErrorContains: "read AGENTS.md",
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
			wantError:         true,
			wantErrorContains: "invalid filesystem root",
			skipWorkspace:     true,
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
			want:            func(_ *testing.T, _, _ string) string { return fileInstructionsMarker },
			wantConfigCount: 1,
		},
		{
			name: "explicit raw text wins over AGENTS.md",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				return rawInstructionsMarker
			},
			want:            func(_ *testing.T, _, _ string) string { return rawInstructionsMarker },
			wantConfigCount: 1,
		},
		{
			name: "missing prompt path is literal text",
			setup: func(t *testing.T, workspaceDir string) string {
				t.Helper()
				writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
				return filepath.Join(workspaceDir, "missing-prompt.md")
			},
			want:            func(_ *testing.T, _, explicit string) string { return explicit },
			wantConfigCount: 1,
		},
	}
}

func TestRunSessionWithInstructions_SourceMatrix(t *testing.T) {
	for _, tt := range instructionSourceCases() {
		t.Run(tt.name, func(t *testing.T) { runInstructionSourceCase(t, tt) })
	}
}

// runInstructionSourceCase resolves one instruction source through the public
// entry point and asserts either the delivered instructions or a prompt error
// that stops before any provider connection.
func runInstructionSourceCase(t *testing.T, tt instructionSourceCase) {
	t.Helper()
	workspaceDir := filepath.Join(t.TempDir(), "workspace")
	if !tt.skipWorkspace {
		if err := os.Mkdir(workspaceDir, 0755); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
	}
	explicit := tt.setup(t, workspaceDir)
	inferencer := newSessionInstructionsTestInferencer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := agentruntime.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		Prompt:            userTurnMarker,
		SessionInferencer: inferencer,
	}, explicit)
	if tt.wantError {
		assertPromptResolutionError(t, err, tt.wantErrorContains, inferencer)
		return
	}
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}
	assertSessionInstructionEvents(t, inferencer, tt.want(t, workspaceDir, explicit), tt.wantConfigCount)
}

func TestRunSessionWithInstructions_MissingAgentsMDSendsNoToolGroundingOrFile(t *testing.T) {
	workspaceDir := t.TempDir()
	inferencer := newSessionInstructionsTestInferencer()
	toolDefinitions := []messages.ToolDefinition{
		{Name: "read_file", Description: "Read a UTF-8 file from the workspace."},
		{Name: "exec", Description: "Execute a command in the workspace."},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := agentruntime.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		Prompt:            userTurnMarker,
		SessionInferencer: inferencer,
		ToolDefinitions:   toolDefinitions,
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspaceDir, workspace.AgentsMDFileName)); !os.IsNotExist(err) {
		t.Fatalf("missing AGENTS.md gained a side effect: %v", err)
	}
	// Injected provider seams still receive their advertised tools, but the
	// instruction field remains empty: no default or grounding prompt is made.
	assertSessionInstructionEvents(t, inferencer, "", 1)
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

	err := agentruntime.RunSessionWithInstructions(ctx, bytes.NewBuffer(nil), agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
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
	recorder := gwtesting.NewRecordingWebSocketDialer(&recordingRealtimeTestDialer{conn: realtimeConn}, "openai", "gpt-realtime")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := agentruntime.RunSessionWithInstructions(ctx, io.Discard, agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-realtime",
		APIKey:          "test-api-key",
		ConfigDir:       workspaceDir,
		Prompt:          userTurnMarker,
		ToolExecutor:    &messages.DefaultToolExecutor{},
		ToolDefinitions: []messages.ToolDefinition{{Name: "inspect_machine", Description: "Inspect machine state"}},
		WebSocketDialer: recorder,
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	capture := recorder.Capture()
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
		case wireSessionUpdate:
			configCount++
			configIndex = index
			gotInstructions = envelope.Session.Instructions
			toolCount = len(envelope.Session.Tools)
		case wireConversationItemCreate:
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
	cmd := cli.NewSessionCommand(askFlags, globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inferencer}), nil).Generate()
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
	cmd := cli.NewSessionCommand(askFlags, globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inferencer}), nil).Generate()
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
	cmd := cli.NewSessionCommand(askFlags, globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inferencer}), nil).Generate()
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
	if !strings.Contains(out.String(), tools.FilesystemScopeStartupNotice) || strings.Contains(out.String(), "All commands will be allowed") {
		t.Fatalf("startup output does not truthfully describe filesystem and shell boundaries: %q", out.String())
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

func TestRunSessionWithInstructionsAndOptions_PreservesExplicitSeed(t *testing.T) {
	workspaceDir := t.TempDir()
	writeFile(t, filepath.Join(workspaceDir, workspace.AgentsMDFileName), agentsInstructionsMarker)
	inferencer := newSessionInstructionsTestInferencer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := agentruntime.RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, io.Discard, agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
		ReplayPath:        filepath.Join(workspaceDir, "session.json"),
		ConfigDir:         workspaceDir,
		SessionInferencer: inferencer,
	}, "", 0, agentruntime.SessionTextSeed{Value: userTurnMarker, Present: true}, "")
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
	recorder := gwtesting.NewRecordingWebSocketDialer(realtimeDialer, "openai", "gpt-realtime")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := agentruntime.RunSessionWithInstructions(ctx, io.Discard, agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
		Provider:        config.ProviderOpenAI,
		Model:           "gpt-realtime",
		APIKey:          "test-api-key",
		ConfigDir:       workspaceDir,
		Prompt:          userTurnMarker,
		Voice:           "marin",
		WebSocketDialer: recorder,
	}, "")
	if err != nil {
		t.Fatalf("RunSessionWithInstructions: %v", err)
	}

	capture := recorder.Capture()
	got := summarizeOpenAIInitialTurn(t, capture.Records)
	if got.configCount != 1 {
		t.Fatalf("OpenAI instruction-bearing session.update count = %d, want 1; capture=%#v", got.configCount, capture.Records)
	}
	if got.instructions != agentsInstructionsMarker {
		t.Fatalf("OpenAI initial session instructions = %q, want %q", got.instructions, agentsInstructionsMarker)
	}
	if got.voice != "marin" {
		t.Fatalf("OpenAI initial session voice = %q, want marin", got.voice)
	}
	if got.userCount != 1 || got.userText != userTurnMarker {
		t.Fatalf("OpenAI first user event = count %d text %q, want count 1 text %q", got.userCount, got.userText, userTurnMarker)
	}
	if strings.Contains(got.userText, agentsInstructionsMarker) {
		t.Fatalf("OpenAI first user event duplicated session instructions: %q", got.userText)
	}
	if got.configIndex < 0 || got.userIndex < 0 || got.configIndex >= got.userIndex {
		t.Fatalf("OpenAI session.update index = %d, first user index = %d; capture=%#v", got.configIndex, got.userIndex, capture.Records)
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

	err := agentruntime.RunSessionWithInstructions(ctx, io.Discard, agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
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

	err := agentruntime.RunSessionWithInstructions(ctx, &out, agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
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
