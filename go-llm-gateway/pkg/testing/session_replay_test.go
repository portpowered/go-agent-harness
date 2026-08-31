package testing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestSharedCommittedSessionFixtureReplaysDeterministically(t *testing.T) {
	replayer := mustNewSessionReplayer(t, SharedSessionFixturePath("session_text_reply.session.json"), WithReplayOutboundValidation(false))

	var received []messages.StreamMessage
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-replayer.Done():
			goto drain
		case msg := <-replayer.Receive().Chan():
			received = append(received, msg)
		case <-timeout:
			t.Fatal("timed out waiting for shared session fixture replay")
		}
	}

drain:
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			break
		}
		received = append(received, msg)
	}

	expectedTypes := []messages.StreamMessageType{
		messages.StreamTypeSessionCreated,
		messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeSessionClose,
	}
	if len(received) != len(expectedTypes) {
		t.Fatalf("received %d events, want %d", len(received), len(expectedTypes))
	}
	for i, want := range expectedTypes {
		if got := received[i].Type; got != want {
			t.Fatalf("event[%d] type = %q, want %q", i, got, want)
		}
	}

	firstDelta, ok := received[3].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[3] value type = %T, want *messages.TextDeltaValue", received[3].Value)
	}
	secondDelta, ok := received[4].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[4] value type = %T, want *messages.TextDeltaValue", received[4].Value)
	}
	if got := firstDelta.Content + secondDelta.Content; got != "Hello! How can I help you today?" {
		t.Fatalf("replayed text = %q", got)
	}
}

func TestSessionReplayer_ProducesServerToClientEvents(t *testing.T) {
	// Build a capture with both client-to-server and server-to-client events.
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeSessionUpdate, &messages.SessionUpdateValue{Type: "session_update"}),
		makeCapture(DirectionServerToClient, 50, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("hello")),
		makeCapture(DirectionServerToClient, 100, messages.StreamTypeTextEnd, messages.NewTextEndValue()),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer := mustNewSessionReplayer(t, path)

	var received []messages.StreamMessage
	received = append(received, readReplayMessage(t, replayer))

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdate,
		Value: &messages.SessionUpdateValue{Type: "session_update"},
	}) {
		t.Fatalf("Send returned false, expected true: %v", replayer.Err())
	}

	timeout := time.After(2 * time.Second)
	for len(received) < 3 {
		select {
		case msg := <-replayer.Receive().Chan():
			received = append(received, msg)
		case <-replayer.Done():
			for {
				msg, ok := replayer.Receive().Read()
				if !ok {
					break
				}
				received = append(received, msg)
			}
			if len(received) < 3 {
				t.Fatalf("expected 3 events before replay finished, got %d", len(received))
			}
		case <-timeout:
			t.Fatal("timed out waiting for replayer to finish")
		}
	}

	// Should only get the 3 server-to-client events (client-to-server is skipped).
	if len(received) != 3 {
		t.Fatalf("expected 3 events, got %d", len(received))
	}

	if received[0].Type != messages.StreamTypeSessionCreated {
		t.Errorf("event[0] type = %q, want %q", received[0].Type, messages.StreamTypeSessionCreated)
	}
	if received[1].Type != messages.StreamTypeTextDelta {
		t.Errorf("event[1] type = %q, want %q", received[1].Type, messages.StreamTypeTextDelta)
	}
	delta, ok := received[1].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("event[1] value type = %T, want *TextDeltaValue", received[1].Value)
	}
	if delta.Content != "hello" {
		t.Errorf("event[1] content = %q, want %q", delta.Content, "hello")
	}
	if received[2].Type != messages.StreamTypeTextEnd {
		t.Errorf("event[2] type = %q, want %q", received[2].Type, messages.StreamTypeTextEnd)
	}
}

func TestSessionReplayer_SendMatchesExpectedOutboundEvent(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("from client")),
		makeCapture(DirectionServerToClient, 20, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("from server")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer := mustNewSessionReplayer(t, path)

	first := readReplayMessage(t, replayer)
	if first.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("first replay event type = %q, want %q", first.Type, messages.StreamTypeSessionCreated)
	}

	ok := replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("from client"),
	})
	if !ok {
		t.Fatalf("Send returned false, expected true: %v", replayer.Err())
	}

	next := readReplayMessage(t, replayer)
	delta, ok := next.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("next value type = %T, want *TextDeltaValue", next.Value)
	}
	if delta.Content != "from server" {
		t.Fatalf("next delta = %q, want from server", delta.Content)
	}

	sent := replayer.SentLog()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(sent))
	}
}

func TestSessionReplayer_BlocksLaterInboundUntilExpectedOutbound(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("open the gate")),
		makeCapture(DirectionServerToClient, 20, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("after outbound")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer := mustNewSessionReplayer(t, path)

	_ = readReplayMessage(t, replayer)
	select {
	case msg := <-replayer.Receive().Chan():
		t.Fatalf("received %s before expected outbound event was sent", msg.Type)
	case <-time.After(50 * time.Millisecond):
	}

	if !replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("open the gate"),
	}) {
		t.Fatalf("Send returned false, expected true: %v", replayer.Err())
	}

	msg := readReplayMessage(t, replayer)
	delta, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("value type = %T, want *TextDeltaValue", msg.Value)
	}
	if delta.Content != "after outbound" {
		t.Fatalf("delta = %q, want after outbound", delta.Content)
	}
}

func TestSessionReplayer_FailsOnUnexpectedOutboundEvent(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("expected")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer := mustNewSessionReplayer(t, path)

	ok := replayer.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("unexpected"),
	})
	if ok {
		t.Fatal("Send returned true for divergent outbound event")
	}
	if replayer.Err() == nil {
		t.Fatal("expected replay divergence error")
	}
	if !errors.Is(replayer.Err(), providers.ErrReplayMismatch) {
		t.Fatalf("replayer error = %v, want ErrReplayMismatch", replayer.Err())
	}
	if errors.Is(replayer.Err(), providers.ErrProviderRejected) ||
		errors.Is(replayer.Err(), providers.ErrTransport) ||
		errors.Is(replayer.Err(), providers.ErrInvalidRequest) {
		t.Fatalf("replayer error should not match provider failure classes: %v", replayer.Err())
	}
	if got := providers.ErrorClassification(replayer.Err()); got != providers.ErrorClassReplayMismatch {
		t.Fatalf("replayer error classification = %q, want %q", got, providers.ErrorClassReplayMismatch)
	}
	if !errors.Is(replayer.Err(), gateway.ErrReplayMismatch) {
		t.Fatal("divergence should match replay mismatch classification")
	}
	if errors.Is(replayer.Err(), gateway.ErrReplayIncomplete) {
		t.Fatal("divergence should not match replay incomplete classification")
	}
	if errors.Is(replayer.Err(), gateway.ErrTransport) {
		t.Fatal("replay mismatch should not match transport classification")
	}
	if errors.Is(replayer.Err(), gateway.ErrProviderHTTPStatus) {
		t.Fatal("replay mismatch should not match provider HTTP status classification")
	}

	outcome := replayer.Outcome()
	if outcome.Status != SessionReplayDiverged {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, SessionReplayDiverged)
	}
	if outcome.OK() {
		t.Fatal("divergent replay outcome reported OK")
	}
	if !errors.Is(outcome.Err, providers.ErrReplayMismatch) {
		t.Fatalf("outcome err = %v, want ErrReplayMismatch", outcome.Err)
	}
	if outcome.Expected == "" || outcome.Actual == "" {
		t.Fatalf("outcome mismatch detail = expected %q actual %q, want both populated", outcome.Expected, outcome.Actual)
	}
}

func TestSessionReplayer_SendWithOutcomeDistinguishesLifecycleStates(t *testing.T) {
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("unexpected"),
	}

	t.Run("terminal replay failure", func(t *testing.T) {
		events := []CapturedSessionEvent{
			makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("expected")),
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "test.session.json")
		writeCapture(t, path, events)
		replayer := mustNewSessionReplayer(t, path)

		outcome := messages.SendSessionWithOutcome(context.Background(), replayer, msg)
		if outcome.Status != messages.SessionSendTerminalFailure {
			t.Fatalf("status = %q, want %q", outcome.Status, messages.SessionSendTerminalFailure)
		}
		if outcome.OK() {
			t.Fatal("terminal failure outcome reported OK")
		}
		if !errors.Is(outcome.Err, providers.ErrReplayMismatch) {
			t.Fatalf("outcome err = %v, want ErrReplayMismatch", outcome.Err)
		}
	})

	t.Run("closed session", func(t *testing.T) {
		events := []CapturedSessionEvent{
			makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("expected")),
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "test.session.json")
		writeCapture(t, path, events)
		replayer := mustNewSessionReplayer(t, path)
		if err := replayer.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		outcome := messages.SendSessionWithOutcome(context.Background(), replayer, msg)
		if outcome.Status != messages.SessionSendClosed {
			t.Fatalf("status = %q, want %q", outcome.Status, messages.SessionSendClosed)
		}
		if outcome.OK() {
			t.Fatal("closed outcome reported OK")
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		events := []CapturedSessionEvent{
			makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("expected")),
		}

		dir := t.TempDir()
		path := filepath.Join(dir, "test.session.json")
		writeCapture(t, path, events)
		replayer := mustNewSessionReplayer(t, path)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome := messages.SendSessionWithOutcome(ctx, replayer, msg)
		if outcome.Status != messages.SessionSendCancelled {
			t.Fatalf("status = %q, want %q", outcome.Status, messages.SessionSendCancelled)
		}
		if !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", outcome.Err)
		}
	})
}

func TestSessionReplayer_FailsWhenExpectedOutboundIsOmitted(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeSessionCreated, messages.NewSessionCreatedValue("", "")),
		makeCapture(DirectionClientToServer, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("required")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer, err := NewSessionReplayer(path)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}

	_ = readReplayMessage(t, replayer)
	if err := replayer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if replayer.Err() == nil {
		t.Fatal("expected omitted outbound event error")
	}
	if !errors.Is(replayer.Err(), providers.ErrReplayIncomplete) {
		t.Fatalf("replayer error = %v, want ErrReplayIncomplete", replayer.Err())
	}
	if errors.Is(replayer.Err(), providers.ErrReplayMismatch) {
		t.Fatal("omitted outbound should not match replay mismatch classification")
	}
	if got := providers.ErrorClassification(replayer.Err()); got != providers.ErrorClassReplayIncomplete {
		t.Fatalf("replayer error classification = %q, want %q", got, providers.ErrorClassReplayIncomplete)
	}
	if !errors.Is(replayer.Err(), gateway.ErrReplayIncomplete) {
		t.Fatal("omitted outbound should match replay incomplete classification")
	}
	if errors.Is(replayer.Err(), gateway.ErrReplayMismatch) {
		t.Fatal("omitted outbound should not match replay mismatch classification")
	}
	if errors.Is(replayer.Err(), gateway.ErrTransport) {
		t.Fatal("omitted outbound should not match transport classification")
	}
	if errors.Is(replayer.Err(), gateway.ErrProviderHTTPStatus) {
		t.Fatal("omitted outbound should not match provider HTTP status classification")
	}
	var incompleteErr *gateway.ReplayIncompleteError
	if !errors.As(replayer.Err(), &incompleteErr) {
		t.Fatal("omitted outbound should expose typed replay incomplete details")
	}

	outcome := replayer.Outcome()
	if outcome.Status != SessionReplayIncomplete {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, SessionReplayIncomplete)
	}
	if outcome.OK() {
		t.Fatal("incomplete replay outcome reported OK")
	}
	if !errors.Is(outcome.Err, providers.ErrReplayIncomplete) {
		t.Fatalf("outcome err = %v, want ErrReplayIncomplete", outcome.Err)
	}
	if outcome.Expected == "" || outcome.Actual != "replay close" {
		t.Fatalf("outcome mismatch detail = expected %q actual %q, want omitted outbound close detail", outcome.Expected, outcome.Actual)
	}
}

func TestSessionReplayer_OutcomeReportsSuccessfulReplayCompletion(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("complete")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	replayer := mustNewSessionReplayer(t, path)
	<-replayer.Done()

	outcome := replayer.Outcome()
	if outcome.Status != SessionReplayCompleted {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, SessionReplayCompleted)
	}
	if !outcome.OK() {
		t.Fatal("completed replay outcome did not report OK")
	}
	if outcome.Err != nil {
		t.Fatalf("outcome err = %v, want nil", outcome.Err)
	}
}

func TestSessionReplayer_ReplaysFlushedCaptureToCompletionOutcome(t *testing.T) {
	fake := newFakeSession()
	rec := NewSessionRecorder(fake, WithSessionCaptureProvider("grok", "grok-realtime"))

	fake.inbound.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("flushed"),
	})
	if _, ok := readRecordedMessage(t, rec); !ok {
		t.Fatal("expected recorder relay to capture inbound message")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "capture.session.json")
	if err := rec.FlushToFile(path); err != nil {
		t.Fatalf("FlushToFile: %v", err)
	}

	replayer := mustNewSessionReplayer(t, path)
	<-replayer.Done()

	msg, ok := replayer.Receive().Read()
	if !ok {
		t.Fatal("expected replayed flushed capture message")
	}
	delta, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("replayed value type = %T, want *TextDeltaValue", msg.Value)
	}
	if delta.Content != "flushed" {
		t.Fatalf("replayed content = %q, want flushed", delta.Content)
	}
	if outcome := replayer.Outcome(); outcome.Status != SessionReplayCompleted {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, SessionReplayCompleted)
	}
}

func TestSessionReplayer_SkipTimingDefault(t *testing.T) {
	// Create events with large gaps — they should replay instantly without WithReplayTiming.
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("a")),
		makeCapture(DirectionServerToClient, 5000, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("b")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	start := time.Now()
	replayer := mustNewSessionReplayer(t, path) // no WithReplayTiming

	<-replayer.Done()
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("replay took %v, expected near-instant (no timing)", elapsed)
	}
}

func TestSessionReplayer_AcceptsLegacyEventArray(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("legacy")),
	}
	events[0].PayloadType = ""
	events[0].Data = events[0].Payload
	events[0].Payload = nil

	data, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}

	replayer := mustNewSessionReplayerFromLegacyBytes(t, data)
	<-replayer.Done()

	msg, ok := replayer.Receive().Read()
	if !ok {
		t.Fatal("expected one replayed legacy event")
	}
	delta, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("value type = %T, want *TextDeltaValue", msg.Value)
	}
	if delta.Content != "legacy" {
		t.Errorf("content = %q, want legacy", delta.Content)
	}
}

func TestSessionReplayer_StopsDeliveryWhenOwnedContextCanceled(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionServerToClient, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("first")),
		makeCapture(DirectionServerToClient, 200, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("second")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	ctx, cancel := context.WithCancel(context.Background())
	replayer := mustNewSessionReplayer(t, path, WithReplayContext(ctx), WithReplayTiming())

	first := readReplayMessage(t, replayer)
	firstDelta, ok := first.Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("first value type = %T, want *TextDeltaValue", first.Value)
	}
	if firstDelta.Content != "first" {
		t.Fatalf("first delta = %q, want first", firstDelta.Content)
	}

	cancel()

	select {
	case msg := <-replayer.Receive().Chan():
		t.Fatalf("unexpected replayed message after cancellation: %s", msg.Type)
	case <-replayer.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replay cancellation to stop delivery")
	}

	select {
	case msg := <-replayer.Receive().Chan():
		t.Fatalf("unexpected queued message after replay cancellation: %s", msg.Type)
	default:
	}
}

func TestSessionReplayer_CancellationWakesExpectedOutboundWait(t *testing.T) {
	events := []CapturedSessionEvent{
		makeCapture(DirectionClientToServer, 0, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("required outbound")),
		makeCapture(DirectionServerToClient, 10, messages.StreamTypeTextDelta, messages.NewTextDeltaValue("after outbound")),
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.session.json")
	writeCapture(t, path, events)

	ctx, cancel := context.WithCancel(context.Background())
	replayer := mustNewSessionReplayer(t, path, WithReplayContext(ctx))
	cancel()

	select {
	case <-replayer.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replay cancellation while blocked on expected outbound event")
	}

	outcome := replayer.Outcome()
	if outcome.Status != SessionReplayCancelled {
		t.Fatalf("outcome status = %q, want %q", outcome.Status, SessionReplayCancelled)
	}
	if !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("outcome err = %v, want context.Canceled", outcome.Err)
	}
	if errors.Is(outcome.Err, providers.ErrReplayMismatch) ||
		errors.Is(outcome.Err, providers.ErrReplayIncomplete) ||
		errors.Is(outcome.Err, providers.ErrProviderRejected) ||
		errors.Is(outcome.Err, providers.ErrTransport) {
		t.Fatalf("cancellation should not match replay or provider failure classes: %v", outcome.Err)
	}
	if errors.Is(outcome.Err, gateway.ErrReplayMismatch) ||
		errors.Is(outcome.Err, gateway.ErrReplayIncomplete) ||
		errors.Is(outcome.Err, gateway.ErrProviderHTTPStatus) ||
		errors.Is(outcome.Err, gateway.ErrTransport) {
		t.Fatalf("cancellation should not match gateway replay/provider failure classes: %v", outcome.Err)
	}
	if got := providers.ErrorClassification(outcome.Err); got != providers.ErrorClassCancellation {
		t.Fatalf("outcome error classification = %q, want %q", got, providers.ErrorClassCancellation)
	}
	if outcome.OK() {
		t.Fatal("cancelled replay outcome reported OK")
	}
}

// --- helpers ---

func makeCapture(dir SessionEventDirection, tsMs int64, msgType messages.StreamMessageType, val messages.StreamMessageValue) CapturedSessionEvent {
	msg := messages.StreamMessage{Type: msgType, Value: val}
	data, _ := MarshalStreamMessage(msg)
	return CapturedSessionEvent{
		Direction:   dir,
		TimestampMs: tsMs,
		Type:        string(msgType),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     data,
	}
}

func writeCapture(t *testing.T, path string, events []CapturedSessionEvent) {
	t.Helper()
	for i := range events {
		events[i].Sequence = i + 1
	}
	data, err := json.MarshalIndent(SessionCapture{
		Version:  SessionCaptureVersion,
		Provider: SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
		Session:  SessionMetadata{StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano)},
		Records:  events,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func readReplayMessage(t *testing.T, replayer *SessionReplayer) messages.StreamMessage {
	t.Helper()
	select {
	case msg := <-replayer.Receive().Chan():
		return msg
	case <-replayer.Done():
		select {
		case msg := <-replayer.Receive().Chan():
			return msg
		default:
		}
		t.Fatalf("replayer finished before next message: %v", replayer.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replay message")
	}
	return messages.StreamMessage{}
}

func mustNewSessionReplayer(t *testing.T, path string, opts ...SessionReplayerOption) *SessionReplayer {
	t.Helper()

	replayer, err := NewSessionReplayer(path, opts...)
	if err != nil {
		t.Fatalf("NewSessionReplayer: %v", err)
	}
	t.Cleanup(func() {
		if err := replayer.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return replayer
}

func mustNewSessionReplayerFromLegacyBytes(t *testing.T, data []byte, opts ...SessionReplayerOption) *SessionReplayer {
	t.Helper()

	replayer, err := NewSessionReplayerFromLegacyBytes(data, opts...)
	if err != nil {
		t.Fatalf("NewSessionReplayerFromLegacyBytes: %v", err)
	}
	t.Cleanup(func() {
		if err := replayer.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return replayer
}
