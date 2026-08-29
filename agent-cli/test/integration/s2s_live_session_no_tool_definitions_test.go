package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	strictOpenAIExecAdvertisementMarker = "PROBE_TOOL_MARKER_9182"
	strictOpenAIExecCallID              = "call_exec_probe_1"
	strictOpenAIExecOutput              = strictOpenAIExecAdvertisementMarker + "\n"
	strictOpenAIExecContinuation        = "strict replay continuation"
)

// TestSessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay proves
// the shipped CLI's provider-facing advertisement and tool round trip. The
// production composition root owns the registry and executor; the replay is
// only the deterministic external transport boundary. It strictly checks the
// initial registry-derived schema, accepts one provider function call, and
// requires the exact correlated result before the continuation can complete.
func TestSessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay(t *testing.T) {
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, true)
	invocationLogPath := filepath.Join(configDir, "exec-invocations.log")
	execCommand := fmt.Sprintf(
		"echo %s >> %s; echo %s",
		strictOpenAIExecAdvertisementMarker,
		strictOpenAIShellQuote(invocationLogPath),
		strictOpenAIExecAdvertisementMarker,
	)
	capturePath := filepath.Join(t.TempDir(), "openai-exec-round-trip.session.json")
	writeStrictOpenAIExecRoundTripCapture(t, capturePath, execCommand)

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production agent CLI: %v", err)
	}
	writer := NewTestWriter()
	root := agentCLI.Generate()
	root.SetOut(writer.Stdout())
	root.SetErr(writer.Stderr())
	root.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--replay", capturePath,
		"probe", strictOpenAIExecAdvertisementMarker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("production session replay rejected registry-derived exec round trip: %v\nstderr=%s", err, writer.StderrString())
	}
	stdout := writer.StdoutString()
	if !strings.Contains(stdout, strictOpenAIExecContinuation) {
		t.Fatalf("strict replay completed without post-result continuation %q: %q", strictOpenAIExecContinuation, stdout)
	}
	invocations, err := os.ReadFile(invocationLogPath)
	if err != nil {
		t.Fatalf("read default exec invocation log: %v", err)
	}
	if got := string(invocations); got != strictOpenAIExecOutput {
		t.Fatalf("default exec invocation log = %q, want exactly one marker line %q", got, strictOpenAIExecOutput)
	}
}

func TestSessionCommand_StrictOpenAIReplayRejectsRecordedExecCallWithoutCurrentAllowlist(t *testing.T) {
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, false)
	execCommand := fmt.Sprintf("echo %s", strictOpenAIExecAdvertisementMarker)
	capturePath := filepath.Join(t.TempDir(), "openai-exec-round-trip.session.json")
	writeStrictOpenAIExecRoundTripCapture(t, capturePath, execCommand)

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production agent CLI: %v", err)
	}
	root := agentCLI.Generate()
	root.SetOut(NewTestWriter().Stdout())
	root.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--replay", capturePath,
		"probe", strictOpenAIExecAdvertisementMarker,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("strict replay unexpectedly accepted a session without exec definitions")
	}
	if !strings.Contains(err.Error(), "expected outbound payload for conversation.item.create at sequence 9") {
		t.Fatalf("recorded-tool allowlist error = %v, want strict post-handshake tool-result mismatch", err)
	}
}

func writeSessionToolConfig(t *testing.T, configDir string, execEnabled bool) {
	t.Helper()
	var yaml strings.Builder
	yaml.WriteString("model:\n  provider: openai\n  openai:\n    model: gpt-realtime\ntools:\n  exec:\n    enable_deny_patterns: true\n  list:\n")
	for _, id := range config.DefaultToolIDs {
		enabled := id == "exec" && execEnabled
		fmt.Fprintf(&yaml, "    - id: %s\n      enabled: %t\n", id, enabled)
	}
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(yaml.String()), 0o600); err != nil {
		t.Fatalf("write exec-only session config: %v", err)
	}
}

func writeStrictOpenAIExecRoundTripCapture(t *testing.T, path, execCommand string) {
	t.Helper()
	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  "openai",
			Model: "gpt-realtime",
		},
		Session: gwtesting.SessionMetadata{
			ID:                "sess-strict-exec-round-trip",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: []gwtesting.CapturedSessionEvent{
			strictOpenAIWebSocketRecord(1, gwtesting.DirectionClientToServer, "session.update", `{"type":"session.update","session":{"type":"realtime","model":"gpt-realtime","tools":[{"type":"function","name":"exec","description":"Execute a shell command and return its output. Use with caution.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute"},"working_dir":{"type":"string","description":"Optional working directory for the command"}},"required":["command"]}}]}}`),
			strictOpenAIWebSocketRecord(2, gwtesting.DirectionServerToClient, "session.created", `{"type":"session.created","session":{"id":"sess-strict-exec-round-trip","model":"gpt-realtime"}}`),
			strictOpenAIWebSocketRecord(3, gwtesting.DirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"probe PROBE_TOOL_MARKER_9182"}]}}`),
			strictOpenAIWebSocketRecord(4, gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`),
			strictOpenAIWebSocketRecord(5, gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"resp-strict-exec-call"}}`),
			strictOpenAIWebSocketRecord(6, gwtesting.DirectionServerToClient, "response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","id":"item-strict-exec-1","call_id":"`+strictOpenAIExecCallID+`","name":"exec"}}`),
			strictOpenAIWebSocketRecord(7, gwtesting.DirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"`+strictOpenAIExecCallID+`","name":"exec","arguments":`+strictOpenAIJSONQuote(fmt.Sprintf(`{"command":%s}`, strictOpenAIJSONQuote(execCommand)))+`}`),
			strictOpenAIWebSocketRecord(8, gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"resp-strict-exec-call","status":"completed"}}`),
			strictOpenAIWebSocketRecord(9, gwtesting.DirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"`+strictOpenAIExecCallID+`","output":`+strictOpenAIJSONQuote(strictOpenAIExecOutput)+`}}`),
			strictOpenAIWebSocketRecord(10, gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`),
			strictOpenAIWebSocketRecord(11, gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"resp-strict-exec-continuation"}}`),
			strictOpenAIWebSocketRecord(12, gwtesting.DirectionServerToClient, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"`+strictOpenAIExecContinuation+`"}`),
			strictOpenAIWebSocketRecord(13, gwtesting.DirectionServerToClient, "response.output_text.done", `{"type":"response.output_text.done"}`),
			strictOpenAIWebSocketRecord(14, gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"resp-strict-exec-continuation","status":"completed"}}`),
			strictOpenAIWebSocketRecord(15, gwtesting.DirectionServerToClient, "session.closed", `{"type":"session.closed","session_id":"sess-strict-exec-round-trip","reason":"fixture_complete"}`),
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal strict OpenAI exec round-trip capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write strict OpenAI exec round-trip capture: %v", err)
	}
}

func strictOpenAIJSONQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func strictOpenAIShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func strictOpenAIWebSocketRecord(sequence int, direction gwtesting.SessionEventDirection, eventType, payload string) gwtesting.CapturedSessionEvent {
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: int64(sequence),
		Type:        eventType,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(payload),
	}
}
