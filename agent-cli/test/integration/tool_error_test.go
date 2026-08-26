package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// s2s-v4c-tool-error vertical: a failing tool call must surface a typed ERROR
// event on the observed delta stream of the real CLI command while the process
// survives (no panic, no hang, successful exit). Negative controls prove the
// assertions discriminate against panic and silently-dropped-error regressions.

const toolErrorFixtureName = "tool_error_read_file.json"

const toolErrorPrompt = "Please read the file /nonexistent/s2s-v4c/missing.txt using the read_file tool."

type streamEventLine struct {
	Type  string          `json:"type"`
	Index int             `json:"index,omitempty"`
	Role  string          `json:"role,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

type errorValueJSON struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func parseStreamEvents(t *testing.T, stdout string) []streamEventLine {
	t.Helper()
	var events []streamEventLine
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var evt streamEventLine
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("parse NDJSON stream event %q: %v", line, err)
		}
		events = append(events, evt)
	}
	return events
}

// requireTypedToolError finds the typed tool-error event on the observed delta
// stream. The returned error names exactly what is missing so silent-swallow
// regressions fail with an actionable message.
func requireTypedToolError(events []streamEventLine) error {
	for _, evt := range events {
		if evt.Type != "ERROR" {
			continue
		}
		var value errorValueJSON
		if json.Unmarshal(evt.Value, &value) != nil {
			continue
		}
		if value.Type == "error" && strings.Contains(value.Message, "v4c_unknown_tool") && strings.Contains(value.Message, "failed") {
			return nil
		}
	}
	return errors.New(
		"missing expected typed tool-error event on the delta stream: wanted an ERROR event with " +
			"value.type=\"error\" whose message identifies the failed v4c_unknown_tool tool call, but no such event was emitted")
}

func newToolErrorConfigDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := tmpDir + string(os.PathSeparator) + config.ConfigFileName
	configYAML := `model:
  provider: openrouter
  openrouter:
    model: z-ai/glm-4.7
    api_key: replay-dummy
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return tmpDir
}

// TestFailingToolCallEmitsTypedDeltaErrorAndSessionSurvives replays a recorded
// fixture in which the model issues a read_file tool call whose execution
// fails, drives the real ask CLI command over the hermetic replay transport,
// and asserts the typed tool-error event appears on the delta stream while the
// session terminates normally.
func TestFailingToolCallEmitsTypedDeltaErrorAndSessionSurvives(t *testing.T) {
	configDir := newToolErrorConfigDir(t)

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	tw := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{
		"ask",
		"--config-dir", configDir,
		"--system-prompt", "none",
		"--no-system-information",
		"--stream",
		"--output-json",
		"--replay", locateCLIFixture(t, toolErrorFixtureName),
		toolErrorPrompt,
	})

	runDone := make(chan error, 1)
	go func() { runDone <- rootCmd.ExecuteContext(context.Background()) }()
	select {
	case execErr := <-runDone:
		if execErr != nil {
			t.Fatalf("ask --replay should survive the failing tool call and exit successfully: %v\nstdout:\n%s", execErr, tw.StdoutString())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("ask --replay hung after the failing tool call (no exit within 60s)")
	}

	events := parseStreamEvents(t, tw.StdoutString())
	if err := requireTypedToolError(events); err != nil {
		t.Fatalf("%v\nobserved events: %+v", err, events)
	}

	output := tw.StdoutString() + tw.StderrString()
	if strings.Contains(strings.ToLower(output), "panic") {
		t.Fatalf("session output must not contain a panic, got:\n%s", output)
	}
	sawTextDeltas := false
	for _, evt := range events {
		if evt.Type == "TEXT.DELTA" || evt.Type == "MESSAGE.START" {
			sawTextDeltas = true
		}
	}
	if !sawTextDeltas {
		t.Fatalf("expected the replayed assistant turn to produce delta events before the tool error")
	}
}

// --- negative controls -----------------------------------------------------

// toolCallInferencer is a hermetic inferencer override that issues the same
// doomed read_file tool call as the recorded fixture on its first invocation
// and answers with plain text afterwards, without any HTTP.
type toolCallInferencer struct{ calls int }

func (f *toolCallInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	f.calls++
	if f.calls > 1 {
		return messages.InferenceResult{
			Message: messages.NewTextMessage(messages.RoleAssistant, "the tool path has been exercised"),
		}, nil
	}
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, "reading the file now"),
		ToolCalls: []messages.ToolCall{{
			ID:        "call_v4c_read_missing",
			Name:      "v4c_unknown_tool",
			Arguments: `{"path":"/nonexistent/s2s-v4c/missing.txt"}`,
		}},
	}, nil
}

func (toolCallInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, errors.New("InferStream not used by the s2s-v4c tool-error controls")
}

// swallowingToolExecutor drops every tool failure on the floor and reports
// success with empty content — the silent-swallow regression class.
type swallowingToolExecutor struct{}

func (swallowingToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: "swallowed failure reported as success"}, nil
}

// panickingToolExecutor crashes the process the way an unhandled panic in a
// real tool would.
type panickingToolExecutor struct{}

func (panickingToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	panic("s2s-v4c negative control: unhandled panic inside tool execution")
}

func runOverrideCLI(t *testing.T, executor messages.ToolExecutor) (string, string, error) {
	t.Helper()
	globalFlags := flags.NewGlobalFlags()
	rootCommand := cli.NewRootCommand(globalFlags)
	registry := tools.NewToolRegistryFromConfig(nil)
	exec := agent.NewExecutor(executor, services.DefaultToolDefs(registry), &toolCallInferencer{}, true)
	askFlags := flags.NewAskFlags()
	loopFlags := flags.NewLoopFlags()
	router := cli.NewRouter(
		globalFlags,
		rootCommand,
		cli.NewAskCommand(exec, askFlags, loopFlags, globalFlags),
		cli.NewChatCommand(exec, askFlags, loopFlags, flags.NewChatFlags(), globalFlags),
		cli.NewToolCommand(globalFlags),
		cli.NewInteractionCommand(),
		cli.NewInteractionReplayCommand(),
		cli.NewProbeCommand(),
		cli.NewProbeRunCommand(),
		cli.NewProbeGateCommand(),
		cli.NewSessionCommand(askFlags, globalFlags, nil, nil),
		cli.NewSessionShowCommand(globalFlags),
		cli.NewSessionListCommand(globalFlags),
		cli.NewSessionDeleteCommand(globalFlags),
		cli.NewConfigCommand(),
		cli.NewConfigAddLocalCommand(globalFlags),
	)
	tw := NewTestWriter()
	rootCmd := router.BuildRoot()
	rootCmd.SetOut(tw.Stdout())
	rootCmd.SetErr(tw.Stderr())
	rootCmd.SetArgs([]string{
		"ask",
		"--config-dir", newToolErrorConfigDir(t),
		"--system-prompt", "none",
		"--no-system-information",
		"--stream",
		"--output-json",
		toolErrorPrompt,
	})
	execErr := rootCmd.ExecuteContext(context.Background())
	return tw.StdoutString(), tw.StderrString(), execErr
}

// TestNegativeControlSuppressedToolErrorFailsScenario proves that dropping the
// tool error so it never reaches the delta stream makes the typed-event
// assertion fail with a message that names the missing event.
func TestNegativeControlSuppressedToolErrorFailsScenario(t *testing.T) {
	stdout, stderr, execErr := runOverrideCLI(t, swallowingToolExecutor{})
	if execErr != nil {
		t.Fatalf("suppressed-error control should still run to completion: %v\nstderr:\n%s", execErr, stderr)
	}
	events := parseStreamEvents(t, stdout)
	err := requireTypedToolError(events)
	if err == nil {
		t.Fatalf("negative control failed: the suppressed tool error was NOT detected — " +
			"a dropped error must never pass this vertical")
	}
	if !strings.Contains(err.Error(), "missing expected typed tool-error event") {
		t.Fatalf("negative-control failure should name the missing typed tool-error event, got: %v", err)
	}
}

// TestNegativeControlUnhandledPanicFailsScenario re-runs this test binary as a
// subprocess with an executor that panics inside the tool path and asserts the
// scenario fails explicitly as detected panic (crash, not timeout).
func TestNegativeControlUnhandledPanicFailsScenario(t *testing.T) {
	if os.Getenv(toolErrorPanicHelperEnv) == "" {
		t.Skip("negative-control helper; runs only as a subprocess of itself")
	}
	stdout, stderr, execErr := runOverrideCLI(t, panickingToolExecutor{})
	_ = stdout
	_ = stderr
	_ = execErr // unreachable when the panic propagates; kept for symmetry
}

const toolErrorPanicHelperEnv = "S2S_V4C_TOOL_ERROR_PANIC_HELPER"

// TestNegativeControlUnhandledPanicDetectedByParent drives the subprocess and
// verifies the panic manifests as an explicit crash with a panic report rather
// than a hang or clean exit.
func TestNegativeControlUnhandledPanicDetectedByParent(t *testing.T) {
	if os.Getenv(toolErrorPanicHelperEnv) != "" {
		t.Skip("parent-side assertion for the panic control")
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestNegativeControlUnhandledPanicFailsScenario", "-test.count=1")
	cmd.Env = append(os.Environ(), toolErrorPanicHelperEnv+"=1")
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start panic helper subprocess: %v", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("panic control timed out instead of failing fast with a detected panic")
	}

	output := out.String()
	if !strings.Contains(output, "panic:") {
		t.Fatalf("expected the helper to crash with an explicit panic report, got exit output:\n%s", output)
	}
	if !strings.Contains(output, "unhandled panic inside tool execution") {
		t.Fatalf("panic report should identify the panicking tool execution, got:\n%s", output)
	}
}
