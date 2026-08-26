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

const strictOpenAIExecAdvertisementMarker = "PROBE_TOOL_MARKER_9182"

// TestSessionCommand_DefaultRegistryAdvertisesExecInStrictOpenAIReplay proves
// the shipped CLI's provider-facing advertisement path. The production
// composition root owns the registry and executor; the replay is only the
// deterministic external transport boundary. Its first outbound event is a
// strict assertion of the registry-derived OpenAI exec schema.
func TestSessionCommand_DefaultRegistryAdvertisesExecInStrictOpenAIReplay(t *testing.T) {
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, true)
	capturePath := filepath.Join(t.TempDir(), "openai-exec-advertisement.session.json")
	writeStrictOpenAIExecAdvertisementCapture(t, capturePath)

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
		t.Fatalf("production session replay rejected registry-derived exec advertisement: %v\nstderr=%s", err, writer.StderrString())
	}
	if !strings.Contains(writer.StdoutString(), "strict replay response") {
		t.Fatalf("strict replay completed without its terminal response: %q", writer.StdoutString())
	}
}

func TestSessionCommand_StrictOpenAIReplayRejectsMissingExecAdvertisement(t *testing.T) {
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, false)
	capturePath := filepath.Join(t.TempDir(), "openai-exec-advertisement.session.json")
	writeStrictOpenAIExecAdvertisementCapture(t, capturePath)

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
	if !strings.Contains(err.Error(), "expected outbound payload for session.update") {
		t.Fatalf("missing-advertisement error = %v, want expected-versus-actual session.update mismatch", err)
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

func writeStrictOpenAIExecAdvertisementCapture(t *testing.T, path string) {
	t.Helper()
	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  "openai",
			Model: "gpt-realtime",
		},
		Session: gwtesting.SessionMetadata{
			ID:                "sess-strict-exec-advertisement",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: []gwtesting.CapturedSessionEvent{
			strictOpenAIWebSocketRecord(1, gwtesting.DirectionClientToServer, "session.update", `{"type":"session.update","session":{"type":"realtime","model":"gpt-realtime","tools":[{"type":"function","name":"exec","description":"Execute a shell command and return its output. Use with caution.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute"},"working_dir":{"type":"string","description":"Optional working directory for the command"}},"required":["command"]}}]}}`),
			strictOpenAIWebSocketRecord(2, gwtesting.DirectionServerToClient, "session.created", `{"type":"session.created","session":{"id":"sess-strict-exec-advertisement","model":"gpt-realtime"}}`),
			strictOpenAIWebSocketRecord(3, gwtesting.DirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"probe PROBE_TOOL_MARKER_9182"}]}}`),
			strictOpenAIWebSocketRecord(4, gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`),
			strictOpenAIWebSocketRecord(5, gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created"}`),
			strictOpenAIWebSocketRecord(6, gwtesting.DirectionServerToClient, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"strict replay response"}`),
			strictOpenAIWebSocketRecord(7, gwtesting.DirectionServerToClient, "response.output_text.done", `{"type":"response.output_text.done"}`),
			strictOpenAIWebSocketRecord(8, gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done"}`),
			strictOpenAIWebSocketRecord(9, gwtesting.DirectionServerToClient, "session.closed", `{"type":"session.closed","session_id":"sess-strict-exec-advertisement","reason":"fixture_complete"}`),
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal strict OpenAI advertisement capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write strict OpenAI advertisement capture: %v", err)
	}
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
