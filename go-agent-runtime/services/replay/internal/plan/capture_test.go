package plan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func captureRecord(direction gatewaytesting.SessionEventDirection, kind, payload string) gatewaytesting.CapturedSessionEvent {
	return gatewaytesting.CapturedSessionEvent{Direction: direction, Type: kind, PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(payload)}
}

func clientRecord(kind, payload string) gatewaytesting.CapturedSessionEvent {
	return captureRecord(gatewaytesting.DirectionClientToServer, kind, payload)
}

func serverRecord(kind, payload string) gatewaytesting.CapturedSessionEvent {
	return captureRecord(gatewaytesting.DirectionServerToClient, kind, payload)
}

func writePlanCapture(t *testing.T, records ...gatewaytesting.CapturedSessionEvent) string {
	t.Helper()
	for i := range records {
		records[i].Sequence = i + 1
		records[i].TimestampMs = int64(i)
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "fixture"},
		Session:  gatewaytesting.SessionMetadata{StartedAtUTC: "2026-01-01T00:00:00Z"},
		Records:  records,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "capture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlannerPreservesTextAndSetupOrdering(t *testing.T) {
	for _, text := range []string{"hello", ""} {
		payload, err := json.Marshal(map[string]any{"type": "conversation.item.create", "item": map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": text}}}})
		if err != nil {
			t.Fatal(err)
		}
		path := writePlanCapture(t,
			clientRecord("session.update", `{"type":"session.update","session":{"audio":{"input":{"format":{"rate":16000}},"output":{"format":{"rate":24000}}}}}`),
			serverRecord("session.updated", `{"type":"session.updated"}`),
			clientRecord("conversation.item.create", string(payload)),
			clientRecord("response.create", `{"type":"response.create"}`),
			serverRecord("session.closed", `{"type":"session.closed"}`),
		)
		plan, err := New().LoadLivePlan(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.OpeningPromptPresent || plan.OpeningPrompt != text || !plan.WaitForSessionUpdated {
			t.Fatalf("text/setup plan=%+v", plan)
		}
		if !plan.ProviderCloseExpected || plan.StopAfterResponse || plan.InputAudioSampleRate != 16000 || plan.OutputAudioSampleRate != 24000 {
			t.Fatalf("terminal/format plan=%+v", plan)
		}
	}
}

func TestPlannerSelfDrivesTextInterruptionTruncate(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"c07 interruption"}]}}`),
		clientRecord(replayResponseCreate, `{"type":"response.create"}`),
		serverRecord("response.created", `{"type":"response.created","response":{"id":"resp-c07-interrupted"}}`),
		serverRecord("input_audio_buffer.speech_started", `{"type":"input_audio_buffer.speech_started"}`),
		clientRecord(replayTruncateItem, `{"type":"conversation.item.truncate","item_id":"item-c07-interrupted","content_index":0,"audio_end_ms":123}`),
		serverRecord("response.done", `{"type":"response.done","response":{"id":"resp-c07-interrupted","status":"cancelled"}}`),
		serverRecord("response.created", `{"type":"response.created","response":{"id":"resp-c07-replacement"}}`),
	)

	plan, err := New().LoadLivePlan(t.Context(), path)
	if err != nil {
		t.Fatalf("LoadLivePlan: %v", err)
	}
	if !plan.OpeningPromptPresent || plan.OpeningPrompt != "c07 interruption" {
		t.Fatalf("interruption plan=%+v, want captured opening prompt", plan)
	}
	if !plan.StopAfterResponse || plan.ProviderCloseExpected {
		t.Fatalf("interruption terminal plan=%+v, want response stop without provider close", plan)
	}
	if !plan.InterruptionReplacementExpected {
		t.Fatalf("interruption plan=%+v, want a replacement response boundary", plan)
	}
}

func TestPlannerDoesNotMarkCancelledOnlyReplayAsInterrupted(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"cancelled only"}]}}`),
		clientRecord(replayResponseCreate, `{"type":"response.create"}`),
		serverRecord("response.created", `{"type":"response.created","response":{"id":"resp-cancelled-only"}}`),
		serverRecord("response.done", `{"type":"response.done","response":{"id":"resp-cancelled-only","status":"cancelled"}}`),
	)

	plan, err := New().LoadLivePlan(t.Context(), path)
	if err != nil {
		t.Fatalf("LoadLivePlan: %v", err)
	}
	if plan.InterruptionReplacementExpected {
		t.Fatalf("cancelled-only plan=%+v, want no replacement boundary", plan)
	}
}

func TestPlannerDoesNotWaitForLateSetupAcknowledgement(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
		serverRecord("session.updated", `{"type":"session.updated"}`),
	)
	plan, err := New().LoadLivePlan(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if plan.WaitForSessionUpdated || !plan.StopAfterResponse {
		t.Fatalf("late setup created an admission deadlock: %+v", plan)
	}
}

func TestInspectCaptureRetainsLifecycleMetadataForCallerDrivenActions(t *testing.T) {
	path := writePlanCapture(t,
		clientRecord("session.update", `{"type":"session.update","session":{"audio":{"input":{"format":{"rate":24000}},"output":{"format":{"rate":24000}}}}}`),
		clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
		clientRecord("response.cancel", `{"type":"response.cancel"}`),
		serverRecord("session.closed", `{"type":"session.closed"}`),
	)
	inspection, err := New().InspectCapture(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.LivePlan == nil {
		t.Fatal("caller-driven realtime capture lost lifecycle plan metadata")
	}
	if !inspection.LivePlan.ProviderCloseExpected || inspection.LivePlan.StopAfterResponse {
		t.Fatalf("caller-driven lifecycle plan=%+v, want provider-close wait", *inspection.LivePlan)
	}
	if inspection.LivePlan.InputAudioSampleRate != 24000 || inspection.LivePlan.OutputAudioSampleRate != 24000 {
		t.Fatalf("caller-driven lifecycle rates=%+v, want 24 kHz", *inspection.LivePlan)
	}
}

func TestPlannerAdmissionPreservesCancellationAndErrors(t *testing.T) {
	cause := errors.New("host stopped replay")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := New().LoadLivePlan(ctx, "never-read.json"); !errors.Is(err, cause) {
		t.Fatalf("cancellation cause=%v", err)
	}
	//lint:ignore SA1012 Exercise nil-context rejection before any replay filesystem access.
	if _, err := New().LoadLivePlan(nil, "never-read.json"); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := New().LoadLivePlan(t.Context(), filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing capture accepted")
	}
}

func TestPlannerRejectsMalformedMetadataAndUnsupportedTextActions(t *testing.T) {
	for _, records := range [][]gatewaytesting.CapturedSessionEvent{
		{clientRecord("session.update", `{"type":"session.update","session":{"audio":{"input":{"format":{"rate":"bad"}}}}}`)},
		{clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[]}}`)},
		{clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`), clientRecord("response.cancel", `{"type":"response.cancel"}`)},
	} {
		if _, err := New().LoadLivePlan(t.Context(), writePlanCapture(t, records...)); err == nil {
			t.Fatalf("invalid plan accepted: %+v", records)
		}
	}
}
