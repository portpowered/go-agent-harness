package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	v5aDefaultSleepCallID  = "v5a-default-sleep"
	v5aDefaultSleepArgs    = `{"duration":"0s"}`
	v5aDefaultSleepResult  = "Slept for 0s (no-op)."
	v5aDefaultSleepPrompt  = "invoke the default sleep tool"
	v5aSessionClosedReason = "v5a-default-toolset-complete"
)

// TestSessionCommand_DefaultToolSetActive proves the default sleep tool through
// the production CLI composition. The replay capture blocks the continuation
// until the CLI sends the exact correlated function_call_output for the tool
// call, so the final response and session close also prove result reinjection.
// The production session path must also send its normal follow-up text trigger
// before the replay can deliver the continuation.
func TestSessionCommand_DefaultToolSetActive(t *testing.T) {
	capturePath := writeV5ADefaultSleepCapture(t)
	output, err := executeV5ADefaultSleepSession(t, capturePath, t.TempDir())
	if err != nil {
		var replayErr *gateway.ReplayMismatchError
		if errors.As(err, &replayErr) {
			t.Fatalf("execute production session CLI: %v; replay details: expected=%q actual=%q cause=%v", err, replayErr.Expected, replayErr.Actual, replayErr.Err)
		}
		t.Fatalf("execute production session CLI: %v", err)
	}

	if !strings.Contains(output, "Sleep tool result reinjected.") {
		t.Fatalf("session output is missing the post-tool continuation: %s", output)
	}
	if !strings.Contains(output, "[session closed: "+v5aSessionClosedReason+"]") {
		t.Fatalf("session output is missing normal completion after tool result: %s", output)
	}
}

func TestSessionCommand_DefaultToolSetActive_DisabledSleepRejectsSuccess(t *testing.T) {
	capturePath := writeV5ADefaultSleepCapture(t)
	configDir := writeV5ADisabledSleepConfig(t)
	output, err := executeV5ADefaultSleepSession(t, capturePath, configDir)
	if err == nil {
		t.Fatal("disabled sleep must reject the exact successful correlated result")
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("disabled sleep error = %v, want replay mismatch", err)
	}

	var replayErr *gateway.ReplayMismatchError
	if !errors.As(err, &replayErr) {
		t.Fatalf("disabled sleep error = %v, want typed replay mismatch", err)
	}
	if !strings.Contains(replayErr.Expected, "outbound payload for conversation.item.create at sequence 9") {
		t.Fatalf("disabled sleep expected evidence = %q, want exact tool-result slot", replayErr.Expected)
	}
	if replayErr.Actual != "conversation.item.create" {
		t.Fatalf("disabled sleep actual evidence = %q, want provider result event type", replayErr.Actual)
	}
	if replayErr.Err == nil || !strings.Contains(replayErr.Err.Error(), "does not match actual outbound event") {
		t.Fatalf("disabled sleep mismatch cause = %v, want expected-versus-actual payload evidence", replayErr.Err)
	}
	if strings.Contains(output, "Sleep tool result reinjected.") {
		t.Fatalf("disabled sleep unexpectedly reached the successful continuation: %s", output)
	}
}

func executeV5ADefaultSleepSession(t *testing.T, capturePath, configDir string) (string, error) {
	t.Helper()

	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI: %v", err)
	}

	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--replay", capturePath,
		v5aDefaultSleepPrompt,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	return writer.StdoutString(), err
}

func writeV5ADisabledSleepConfig(t *testing.T) string {
	t.Helper()

	configDir := t.TempDir()
	configPath := filepath.Join(configDir, config.ConfigFileName)
	data := []byte("tools:\n  list:\n    - id: sleep\n      enabled: false\n")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write disabled-sleep config: %v", err)
	}
	return configDir
}

func writeV5ADefaultSleepCapture(t *testing.T) string {
	t.Helper()

	base, err := gwtesting.LoadSessionCapture(filepath.Join("testdata", "openai_realtime_smoke.session.json"))
	if err != nil {
		t.Fatalf("load OpenAI replay baseline: %v", err)
	}
	if len(base.Records) < 2 {
		t.Fatalf("OpenAI replay baseline has %d records, want session handshake", len(base.Records))
	}

	records := append([]gwtesting.CapturedSessionEvent(nil), base.Records[:2]...)
	add := func(direction gwtesting.SessionEventDirection, eventType, payload string) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   direction,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		})
	}

	add(gwtesting.DirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+v5aDefaultSleepPrompt+`"}]}}`)
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"resp_v5a_default_sleep"}}`)
	add(gwtesting.DirectionServerToClient, "response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+v5aDefaultSleepCallID+`","name":"sleep"}}`)
	add(gwtesting.DirectionServerToClient, "response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"`+v5aDefaultSleepCallID+`","name":"sleep","arguments":`+strconv.Quote(v5aDefaultSleepArgs)+`}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"resp_v5a_default_sleep","status":"completed"}}`)
	add(gwtesting.DirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"`+v5aDefaultSleepCallID+`","output":"`+v5aDefaultSleepResult+`"}}`)
	// The stream-only session contract requests the next response by replaying
	// the latest user text after the independently forwarded flat tool result.
	add(gwtesting.DirectionClientToServer, "conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+v5aDefaultSleepPrompt+`"}]}}`)
	add(gwtesting.DirectionClientToServer, "response.create", `{"type":"response.create"}`)
	add(gwtesting.DirectionServerToClient, "response.created", `{"type":"response.created","response":{"id":"resp_v5a_default_sleep_continuation"}}`)
	add(gwtesting.DirectionServerToClient, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"Sleep tool result reinjected."}`)
	add(gwtesting.DirectionServerToClient, "response.output_text.done", `{"type":"response.output_text.done"}`)
	add(gwtesting.DirectionServerToClient, "response.done", `{"type":"response.done","response":{"id":"resp_v5a_default_sleep_continuation","status":"completed"}}`)
	add(gwtesting.DirectionServerToClient, "session.closed", `{"type":"session.closed","session_id":"sess_v5a_default_sleep","reason":"`+v5aSessionClosedReason+`"}`)

	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session: gwtesting.SessionMetadata{
			ID:                "sess_v5a_default_sleep",
			StartedAtUTC:      "2026-08-26T00:00:00Z",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}

	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v5a replay capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "v5a-default-sleep.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v5a replay capture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("validate v5a replay capture: %v", err)
	}
	return path
}
