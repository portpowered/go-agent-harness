package sessionfixturevalidator

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// openAIFunctionCallCapture builds a temp-dir session capture in the OpenAI
// Realtime wire shape: the provider issues a function call
// (response.output_item.added / response.function_call_arguments.done) and the
// client answers with a conversation.item.create carrying a function_call_output.
func openAIFunctionCallOutputCapture(callID string, includeCall bool) gatewaytesting.SessionCapture {
	capture := gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session: gatewaytesting.SessionMetadata{
			ID:                "sess_openai_tool_result",
			StartedAtUTC:      time.Now().UTC().Format(time.RFC3339Nano),
			FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
		},
	}

	record := func(sequence int, direction gatewaytesting.SessionEventDirection, eventType string, payload map[string]any) {
		data, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		capture.Records = append(capture.Records, gatewaytesting.CapturedSessionEvent{
			Sequence:    sequence,
			Direction:   direction,
			TimestampMs: int64(sequence) * 10,
			Type:        eventType,
			PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload:     data,
		})
	}

	if includeCall {
		record(1, gatewaytesting.DirectionServerToClient, "response.output_item.added", map[string]any{
			"item": map[string]any{"type": "function_call", "id": "item_1", "call_id": callID, "name": "lookup_weather"},
		})
		record(2, gatewaytesting.DirectionServerToClient, "response.function_call_arguments.done", map[string]any{
			"call_id":   callID,
			"name":      "lookup_weather",
			"arguments": `{"city":"San Francisco"}`,
		})
	}
	record(3, gatewaytesting.DirectionClientToServer, "conversation.item.create", map[string]any{
		"item": map[string]any{"type": "function_call_output", "call_id": callID, "output": `{"forecast":"sunny"}`},
	})
	record(4, gatewaytesting.DirectionServerToClient, "response.done", map[string]any{})

	return capture
}

func TestRun_OpenAIOriginatedFunctionCallOutputPairsCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openai-tool-result.session.json")
	writeCapture(t, path, openAIFunctionCallOutputCapture("call_oai_1", true))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run([]string{path}, &stdout, &stderr); err != nil {
		t.Fatalf("Run error = %v, want clean validation; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Fatalf("stdout = %q, want success summary", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRun_UnpairedOpenAIFunctionCallOutputStillFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unpaired.session.json")
	writeCapture(t, path, openAIFunctionCallOutputCapture("call_oai_unpaired", false))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{path}, &stdout, &stderr)

	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("Run error = %v, want ErrValidationFailed", err)
	}
	got := stderr.String()
	if !strings.Contains(got, path) || !strings.Contains(got, "has no matching tool call") {
		t.Fatalf("stderr = %q, want unpaired function_call_output rejection", got)
	}
}

func TestRun_EmptyCallIDFunctionCallOutputStillFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-call-id.session.json")
	writeCapture(t, path, openAIFunctionCallOutputCapture("", true))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run([]string{path}, &stdout, &stderr)

	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("Run error = %v, want ErrValidationFailed", err)
	}
	got := stderr.String()
	if !strings.Contains(got, "requires a non-empty persisted call identifier") {
		t.Fatalf("stderr = %q, want empty-call-id rejection", got)
	}
}
