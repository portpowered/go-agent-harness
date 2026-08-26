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
// It is intentionally a red prerequisite canary while the provider-result
// forwarding contract is absent from the current base; do not replace the
// production root with an injected executor to make it pass.
func TestSessionCommand_DefaultToolSetActive(t *testing.T) {
	capturePath := writeV5ADefaultSleepCapture(t)
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI: %v", err)
	}

	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", capturePath,
		"--prompt", v5aDefaultSleepPrompt,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		var replayErr *gateway.ReplayMismatchError
		if errors.As(err, &replayErr) {
			t.Fatalf("execute production session CLI: %v; replay details: expected=%q actual=%q cause=%v", err, replayErr.Expected, replayErr.Actual, replayErr.Err)
		}
		t.Fatalf("execute production session CLI: %v", err)
	}

	got := writer.StdoutString()
	if !strings.Contains(got, "Sleep tool result reinjected.") {
		t.Fatalf("session output is missing the post-tool continuation: %s", got)
	}
	if !strings.Contains(got, "[session closed: "+v5aSessionClosedReason+"]") {
		t.Fatalf("session output is missing normal completion after tool result: %s", got)
	}
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
