package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type recordedConversationToolEvent struct {
	Sequence   uint64 `json:"sequence"`
	Type       string `json:"type"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
	Status     string `json:"status"`
	Content    string `json:"content"`
}

type recordedConversationLog struct {
	ToolEvents []recordedConversationToolEvent `json:"tool_events"`
}

func runToolResultConversationWithBrowserRecording(t *testing.T, wavPath, wirePath string, executor messages.ToolExecutor, enabled bool) (stdout, outputPath, recordDir string, runErr error) {
	t.Helper()
	outputPath = filepath.Join(t.TempDir(), "response.wav")
	recordDir = filepath.Join(t.TempDir(), "recording")
	stdoutBuffer := &testStdoutBuffer{}
	agentCLI, err := wire.InitializeMockAgentCLI(executor, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize agent CLI: %v", err)
	}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdoutBuffer)
	rootCmd.SetErr(io.Discard)
	args := []string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", wirePath,
		"--record-dir", recordDir,
		"--audio-in", wavPath,
		"--audio-out", outputPath,
		"--max-duration", (8 * time.Second).String(),
	}
	if enabled {
		args = append(args,
			"--browser-record=true",
			"--browser-record-arguments=true",
			"--browser-record-results=true",
		)
	}
	rootCmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runErr = rootCmd.ExecuteContext(ctx)
	return stdoutBuffer.String(), outputPath, recordDir, runErr
}

func readRecordedConversationToolEvents(t *testing.T, recordDir string) []recordedConversationToolEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recordDir, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read recorded session log: %v", err)
	}
	var entry recordedConversationLog
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("decode recorded session log: %v\n%s", err, data)
	}
	return entry.ToolEvents
}

func TestSessionToolCallConversationBrowserRecordingParity(t *testing.T) {
	wavPath, reply := conversationFixtureInputs(t)
	wirePath := buildToolResultConversationFixture(t, wavPath, reply, toolResultPositive, true)

	type runSnapshot struct {
		stdout   string
		output   []byte
		tool     []recordedConversationToolEvent
		provider []providerFunctionCallOutput
	}
	runs := make([]runSnapshot, 0, 2)
	for _, enabled := range []bool{false, true} {
		executor := &conversationResultExecutor{result: toolResultPositive}
		stdout, outputPath, recordDir, runErr := runToolResultConversationWithBrowserRecording(t, wavPath, wirePath, executor, enabled)
		if runErr != nil {
			t.Fatalf("browser recording=%t session failed: %v\nstdout:\n%s", enabled, runErr, stdout)
		}
		calls, returned := executor.snapshot()
		if err := validateExactlyOneToolCall(calls); err != nil {
			t.Fatalf("browser recording=%t: %v", enabled, err)
		}
		if len(returned) != 1 || returned[0] != toolResultPositive {
			t.Fatalf("browser recording=%t executor results = %q, want one exact result %s", enabled, returned, toolResultPositive)
		}
		outputs := functionCallOutputsInExchange(t, wirePath)
		if len(outputs) != 1 || outputs[0].CallID != toolConversationCallID || outputs[0].Output != toolResultPositive {
			t.Fatalf("browser recording=%t provider outputs = %#v, want one correlated exact result", enabled, outputs)
		}
		assertToolResultFollowUpOrdering(t, wirePath, outputs[0].Sequence)
		if err := transcriptReflectionError(stdout); err != nil {
			t.Fatalf("browser recording=%t reflection failed: %v", enabled, err)
		}
		evidence := readRecordedConversationToolEvents(t, recordDir)
		if len(evidence) != 2 || evidence[0].Type != "tool_call" || evidence[1].Type != "tool_result" || evidence[0].ToolCallID != toolConversationCallID || evidence[1].ToolCallID != toolConversationCallID || evidence[0].Arguments != toolCallScenarioArguments || evidence[1].Content != toolResultPositive || evidence[1].Status != "completed" {
			t.Fatalf("browser recording=%t session-log tool evidence = %#v, want one exact call/result pair", enabled, evidence)
		}
		output, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("browser recording=%t read output WAV: %v", enabled, err)
		}
		runs = append(runs, runSnapshot{
			stdout:   stdout,
			output:   output,
			tool:     evidence,
			provider: outputs,
		})
	}

	if runs[0].stdout != runs[1].stdout || !bytes.Equal(runs[0].output, runs[1].output) {
		t.Fatalf("browser recording changed the provider/session output: disabled stdout=%q enabled stdout=%q", runs[0].stdout, runs[1].stdout)
	}
	if !bytes.Equal(mustJSON(t, runs[0].tool), mustJSON(t, runs[1].tool)) || !bytes.Equal(mustJSON(t, runs[0].provider), mustJSON(t, runs[1].provider)) {
		t.Fatalf("browser recording changed recorded tool/provider outcomes: disabled=%#v/%#v enabled=%#v/%#v", runs[0].tool, runs[0].provider, runs[1].tool, runs[1].provider)
	}
	t.Logf("browser recording parity: disabled and --browser-record/--browser-record-arguments/--browser-record-results enabled each dispatched %s once, accepted one correlated output before response.create, and retained identical sanitized call/result evidence; recording flags do not affect delivery", toolConversationCallID)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal parity value: %v", err)
	}
	return data
}
