package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
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
	return writePlanCaptureWithDisconnect(t, false, records...)
}

func writePlanCaptureWithDisconnect(t *testing.T, endsWithDisconnect bool, records ...gatewaytesting.CapturedSessionEvent) string {
	t.Helper()
	for i := range records {
		records[i].Sequence = i + 1
		records[i].TimestampMs = int64(i)
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Version:            gatewaytesting.SessionCaptureVersion,
		Provider:           gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "fixture"},
		Session:            gatewaytesting.SessionMetadata{StartedAtUTC: "2026-01-01T00:00:00Z"},
		Records:            records,
		EndsWithDisconnect: endsWithDisconnect,
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

func streamServerRecord(t *testing.T, sequence int, message messages.StreamMessage) gatewaytesting.CapturedSessionEvent {
	t.Helper()
	payload, err := gatewaytesting.MarshalStreamMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	return gatewaytesting.CapturedSessionEvent{
		Sequence: sequence, Direction: gatewaytesting.DirectionServerToClient,
		TimestampMs: int64(sequence), Type: string(message.Type),
		PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage, Payload: payload,
	}
}

func writeStreamReplayCapture(t *testing.T, messagesToReplay ...messages.StreamMessage) string {
	t.Helper()
	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(messagesToReplay))
	for i, message := range messagesToReplay {
		records = append(records, streamServerRecord(t, i+1, message))
	}
	return writeStreamEventsCapture(t, records...)
}

func writeStreamEventsCapture(t *testing.T, records ...gatewaytesting.CapturedSessionEvent) string {
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
	path := filepath.Join(t.TempDir(), "stream-capture.json")
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

func TestPlannerRejectsDuplicateAndMismatchedResponseIdentities(t *testing.T) {
	tests := []struct {
		name    string
		records []gatewaytesting.CapturedSessionEvent
		want    string
	}{
		{
			name: "duplicate created",
			records: []gatewaytesting.CapturedSessionEvent{
				clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"duplicate"}]}}`),
				clientRecord(replayResponseCreate, `{"type":"response.create"}`),
				serverRecord("response.created", `{"type":"response.created","response":{"id":"response-duplicate"}}`),
				serverRecord("response.created", `{"type":"response.created","response":{"id":"response-duplicate"}}`),
			},
			want: "duplicate response.created",
		},
		{
			name: "done does not match active response",
			records: []gatewaytesting.CapturedSessionEvent{
				clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"mismatch"}]}}`),
				clientRecord(replayResponseCreate, `{"type":"response.create"}`),
				serverRecord("response.created", `{"type":"response.created","response":{"id":"response-active"}}`),
				serverRecord("response.done", `{"type":"response.done","response":{"id":"response-other","status":"completed"}}`),
			},
			want: "does not match active response",
		},
		{
			name: "tool item does not match active response",
			records: []gatewaytesting.CapturedSessionEvent{
				clientRecord(replayCreateItem, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"tool mismatch"}]}}`),
				clientRecord(replayResponseCreate, `{"type":"response.create"}`),
				serverRecord("response.created", `{"type":"response.created","response":{"id":"response-active"}}`),
				serverRecord("response.output_item.added", `{"type":"response.output_item.added","response_id":"response-other","item":{"type":"function_call"}}`),
			},
			want: "does not match active response",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writePlanCapture(t, test.records...)
			_, err := New().LoadLivePlan(t.Context(), path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadLivePlan error = %v, want %q", err, test.want)
			}
		})
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
		clientRecord(replayAppend, `{"type":"input_audio_buffer.append","audio":"AQI="}`),
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
	if inspection.Facts.Version != gatewaytesting.SessionCaptureVersion || inspection.Facts.EventCount != 5 || inspection.Facts.ClientAudioAppendCount != 1 {
		t.Fatalf("replay facts=%+v, want version %d, five events, and one client audio append", inspection.Facts, gatewaytesting.SessionCaptureVersion)
	}
	if !inspection.Facts.RealtimeWebSocketReplayable {
		t.Fatalf("realtime capture was admitted without websocket replay validation: %+v", inspection.Facts)
	}
	if len(inspection.Facts.MetricDeltas) != 2 || inspection.Facts.MetricDeltas[0] != (replay.CaptureMetricDelta{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio, Bytes: 2}) || inspection.Facts.MetricDeltas[1] != (replay.CaptureMetricDelta{Direction: metrics.DirectionInput, Modality: metrics.ModalityText, Bytes: 5}) {
		t.Fatalf("capture metric deltas=%+v, want 2 input audio bytes and 5 input text bytes", inspection.Facts.MetricDeltas)
	}
}

func TestCaptureReplayDrainPreservesPublicMessageOrder(t *testing.T) {
	path := writeStreamReplayCapture(t,
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("first")},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("second")},
	)
	replay, err := New().Replay(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	closeReplayTestResource(t, replay, "replay")
	var got []string
	if err := replay.Drain(t.Context(), func(message messages.StreamMessage) error {
		value, ok := message.Value.(*messages.TextDeltaValue)
		if !ok || value == nil {
			t.Fatalf("replayed message value = %#v, want text delta", message.Value)
		}
		got = append(got, value.Content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("replay drain order = %v, want [first second]", got)
	}
}

func TestSessionInferencerReplaysMessagesInOrder(t *testing.T) {
	path := writeStreamReplayCapture(t,
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("first")},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("second")},
	)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var service replay.Service = New()
	inferencer, err := service.NewSessionInferencer(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inferencer.ConnectSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closeReplayTestResource(t, session, "replay session")
	if session.Receive() == nil {
		t.Fatal("replay session has no receive buffer")
	}
	got := readTextReplayMessages(t, ctx, session, 2)
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("session replay order = %v, want [first second]", got)
	}
}

func readTextReplayMessages(t *testing.T, ctx context.Context, session messages.Session, count int) []string {
	t.Helper()
	input := session.Receive().Chan()
	got := make([]string, 0, count)
	for len(got) < count {
		select {
		case message, ok := <-input:
			if !ok {
				t.Fatalf("replay closed after %d messages", len(got))
			}
			value, ok := message.Value.(*messages.TextDeltaValue)
			if !ok || value == nil {
				t.Fatalf("replayed message value = %#v, want text delta", message.Value)
			}
			got = append(got, value.Content)
		case <-session.Done():
			if err := drainAvailableTextMessages(t, input, &got, count); err != nil {
				t.Fatalf("replay completed before %d messages: %v", count, err)
			}
		case <-ctx.Done():
			t.Fatalf("replay did not deliver messages: %v", ctx.Err())
		}
	}
	return got
}

func drainAvailableTextMessages(t *testing.T, input <-chan messages.StreamMessage, got *[]string, count int) error {
	t.Helper()
	for len(*got) < count {
		select {
		case message, ok := <-input:
			if !ok {
				return fmt.Errorf("replay closed after %d messages", len(*got))
			}
			value, ok := message.Value.(*messages.TextDeltaValue)
			if !ok || value == nil {
				return fmt.Errorf("replayed message value = %#v, want text delta", message.Value)
			}
			*got = append(*got, value.Content)
		default:
			return fmt.Errorf("replay buffer has no remaining message")
		}
	}
	return nil
}

func TestSessionInferencerValidatesOutboundBeforeDeliveringInbound(t *testing.T) {
	expected := messages.StreamMessage{Type: messages.StreamTypeResponseCreate}
	payload, err := gatewaytesting.MarshalStreamMessage(expected)
	if err != nil {
		t.Fatal(err)
	}
	inbound := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("recorded")}
	path := writeStreamEventsCapture(t,
		gatewaytesting.CapturedSessionEvent{Direction: gatewaytesting.DirectionClientToServer, Type: string(expected.Type), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage, Payload: payload},
		streamServerRecord(t, 2, inbound),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var service replay.Service = New()
	inferencer, err := service.NewSessionInferencer(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inferencer.ConnectSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closeReplayTestResource(t, session, "replay session")
	if outcome := messages.SendSessionWithOutcome(ctx, session, expected); !outcome.OK() {
		t.Fatalf("recorded outbound send = %+v, want success", outcome)
	}
	message, err := session.Receive().ReadContext(ctx)
	if err != nil {
		t.Fatalf("read recorded inbound message: %v", err)
	}
	value, ok := message.Value.(*messages.TextDeltaValue)
	if !ok || value == nil || value.Content != "recorded" {
		t.Fatalf("inbound replay = %#v, want recorded text delta", message)
	}
	select {
	case <-session.Done():
	case <-ctx.Done():
		t.Fatalf("replay did not finish after ordered events: %v", ctx.Err())
	}
}

func TestSessionInferencerStopsOnOutboundDivergence(t *testing.T) {
	expected := messages.StreamMessage{Type: messages.StreamTypeResponseCreate}
	payload, err := gatewaytesting.MarshalStreamMessage(expected)
	if err != nil {
		t.Fatal(err)
	}
	path := writeStreamEventsCapture(t,
		gatewaytesting.CapturedSessionEvent{Direction: gatewaytesting.DirectionClientToServer, Type: string(expected.Type), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage, Payload: payload},
		streamServerRecord(t, 2, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("must not arrive")}),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var service replay.Service = New()
	inferencer, err := service.NewSessionInferencer(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inferencer.ConnectSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closeReplayTestResource(t, session, "replay session")
	outcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{Type: messages.StreamTypeResponseCancel})
	if outcome.Status != messages.SessionSendTerminalFailure {
		t.Fatalf("divergent outbound outcome = %+v, want terminal failure", outcome)
	}
	var mismatch *gateway.ReplayMismatchError
	if !errors.As(outcome.Err, &mismatch) {
		t.Fatalf("divergent outbound error = %v, want replay mismatch", outcome.Err)
	}
	select {
	case <-session.Done():
	case <-ctx.Done():
		t.Fatalf("replay did not stop after divergence: %v", ctx.Err())
	}
	if _, err := session.Receive().Read(); err {
		t.Fatal("inbound event was delivered after outbound divergence")
	}
}

func TestPrepareLiveReplaysOrderedProviderTrafficAndDisconnect(t *testing.T) {
	prepared := prepareLiveReplayForTest(t)
	closeReplayTestResource(t, prepared, "prepared replay")
	conn, err := prepared.WrapDialer(nil).Dial("ws://capture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatalf("initial session update: %v", err)
	}
	readType := func(want string) {
		t.Helper()
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read %s: %v", want, err)
		}
		var message struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &message); err != nil || message.Type != want {
			t.Fatalf("replayed payload=%s, type error=%v, want %q", payload, err, want)
		}
	}
	readType("session.updated")
	if err := conn.WriteMessage(1, []byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`)); err != nil {
		t.Fatalf("recorded user message: %v", err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatalf("recorded response request: %v", err)
	}
	for _, eventType := range []string{"response.created", "response.done", "session.closed"} {
		readType(eventType)
	}
	if _, _, err := conn.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("replay terminal read error=%v, want EOF", err)
	}
	select {
	case <-prepared.Done():
	default:
		t.Fatal("prepared replay left its completion channel open after recorded disconnect")
	}
	if err := prepared.Err(); err != nil {
		t.Fatalf("completed replay error=%v", err)
	}
}

func TestPrepareLiveReportsOutboundDivergenceAndStops(t *testing.T) {
	prepared := prepareLiveReplayForTest(t)
	closeReplayTestResource(t, prepared, "prepared replay")
	conn, err := prepared.WrapDialer(nil).Dial("ws://capture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatalf("initial session update: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read initial acknowledgement: %v", err)
	}
	if err := conn.WriteMessage(1, []byte(`{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`)); err != nil {
		t.Fatalf("recorded user message: %v", err)
	}
	err = conn.WriteMessage(1, []byte(`{"type":"response.create","response":{"temperature":0}}`))
	var mismatch *gateway.ReplayMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("divergent outbound error=%v, want replay mismatch", err)
	}
	select {
	case <-prepared.Done():
	default:
		t.Fatal("divergent replay left its completion channel open")
	}
	var preparedMismatch *gateway.ReplayMismatchError
	if !errors.As(prepared.Err(), &preparedMismatch) {
		t.Fatalf("prepared replay error=%v, want replay mismatch", prepared.Err())
	}
}

func prepareLiveReplayForTest(t *testing.T) replay.LivePrepared {
	t.Helper()
	path := writePlanCaptureWithDisconnect(t, true,
		clientRecord("session.update", `{"type":"session.update","session":{"modalities":["text"]}}`),
		serverRecord("session.updated", `{"type":"session.updated"}`),
		clientRecord("conversation.item.create", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`),
		clientRecord("response.create", `{"type":"response.create"}`),
		serverRecord("response.created", `{"type":"response.created","response":{"id":"response-1"}}`),
		serverRecord("response.done", `{"type":"response.done","response":{"id":"response-1","status":"completed"}}`),
		serverRecord("session.closed", `{"type":"session.closed"}`),
	)
	var service replay.Service = New()
	prepared, err := service.PrepareLive(t.Context(), replay.LiveRequest{SourcePath: path})
	if err != nil {
		t.Fatalf("prepare live replay: %v", err)
	}
	return prepared
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

func closeReplayTestResource(t *testing.T, closer interface{ Close() error }, description string) {
	t.Helper()
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Errorf("close %s: %v", description, err)
		}
	})
}
