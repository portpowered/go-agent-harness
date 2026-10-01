package openai

// Tests for the outbound wire queue: drop records, response intent dispatch
// under a full or backpressured queue, and deferred audio settlement.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// captureLogger records Warn calls so drop records can be asserted verbatim.
type captureLogger struct {
	mu   sync.Mutex
	warn []map[string]any
}

func (c *captureLogger) Debug(string, ...logging.Field) {}
func (c *captureLogger) Info(string, ...logging.Field)  {}
func (c *captureLogger) Error(string, ...logging.Field) {}
func (c *captureLogger) Fatal(string, ...logging.Field) {}
func (c *captureLogger) Panic(string, ...logging.Field) {}

func (c *captureLogger) Warn(msg string, fields ...logging.Field) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record := map[string]any{"msg": msg}
	for _, field := range fields {
		record[field.Key] = field.Value
	}
	c.warn = append(c.warn, record)
}

func (c *captureLogger) records() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.warn...)
}

func TestRealtimeSessionDropLoggersForceOverflowBothDirections(t *testing.T) {
	logger := &captureLogger{}
	session := newRealtimeSession(newMockWebSocketConn(), logger)
	ctx := context.Background()

	// Audio deltas map 1:1 onto wire events, so 64 sends fill the
	// capacity-64 send queue deterministically.
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0x01}),
	}
	for range 64 {
		if outcome := session.SendWithOutcome(ctx, msg); !outcome.OK() {
			t.Fatalf("queued send returned %+v, want success", outcome)
		}
	}
	// The next write overflows the input path: exactly one drop, one record.
	if outcome := session.SendWithOutcome(ctx, msg); outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("overflowing send returned %+v, want buffer_full", outcome)
	}
	if got := session.InputDrops(); got != 1 {
		t.Fatalf("InputDrops() = %d, want 1", got)
	}
	records := logger.records()
	if len(records) != 1 {
		t.Fatalf("input overflow emitted %d records, want 1", len(records))
	}
	record := records[0]
	if record["msg"] != messages.DropLogMessage {
		t.Errorf("record message = %v, want %q", record["msg"], messages.DropLogMessage)
	}
	if record["direction"] != string(messages.DropDirectionInput) {
		t.Errorf("record direction = %v, want input", record["direction"])
	}
	if record["buffer"] != providers.DropBufferSendQueue {
		t.Errorf("record buffer = %v, want %q", record["buffer"], providers.DropBufferSendQueue)
	}
	if record["count"] != int64(1) {
		t.Errorf("record count = %v (%T), want int64(1)", record["count"], record["count"])
	}
	wantKind := string(models.SessionEventInputAudioBufferAppend)
	if record["type"] != wantKind {
		t.Errorf("record type = %v, want %q", record["type"], wantKind)
	}

	// Overflow the receive buffer: the output-path drop appends a second record.
	receive := session.Receive()
	for range receive.Cap() + 1 {
		receive.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta})
	}
	if got := session.OutputDrops(); got != 1 {
		t.Fatalf("OutputDrops() = %d, want 1", got)
	}
	records = logger.records()
	if len(records) != 2 {
		t.Fatalf("total records after both directions = %d, want 2", len(records))
	}
	outRecord := records[1]
	if outRecord["direction"] != string(messages.DropDirectionOutput) {
		t.Errorf("second record direction = %v, want output", outRecord["direction"])
	}
	if outRecord["count"] != int64(1) {
		t.Errorf("second record count = %v, want int64(1)", outRecord["count"])
	}
	if outRecord["type"] != string(messages.StreamTypeAudioDelta) {
		t.Errorf("second record type = %v, want %q", outRecord["type"], messages.StreamTypeAudioDelta)
	}
}

func TestRealtimeSessionDropLoggersSilentOnNormalTraffic(t *testing.T) {
	logger := &captureLogger{}
	session := newRealtimeSession(newMockWebSocketConn(), logger)
	ctx := context.Background()

	// Audio append events do not open a response, so this normal-traffic probe
	// remains independent of provider lifecycle acknowledgements.
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0x01}),
	}
	for i := range 8 {
		if outcome := session.SendWithOutcome(ctx, msg); !outcome.OK() {
			t.Fatalf("send %d returned %+v, want success", i, outcome)
		}
	}
	if got := session.InputDrops(); got != 0 {
		t.Fatalf("InputDrops() = %d, want 0", got)
	}
	if got := session.OutputDrops(); got != 0 {
		t.Fatalf("OutputDrops() = %d, want 0", got)
	}
	if records := logger.records(); len(records) != 0 {
		t.Fatalf("normal traffic emitted %d drop records, want 0", len(records))
	}
}

func TestRealtimeSession_CancelWaitsForPoppedContinuationAdmission(t *testing.T) {
	// Backpressure on a full send queue freezes the dispatcher after it popped
	// the continuation and before the continuation reached the queue, while it
	// holds the response wire lock: exactly where the old implementation let
	// response.cancel invalidate the generation first.
	session := newConfiguredRealtimeSession(newMockWebSocketConn(), nil, realtimeSessionSettings{writeBackpressure: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer closeForTest(t, session)
	go session.responseIntentLoop(ctx)

	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("initial response admission: %#v", outcome)
	}
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-active"}}`),
	})
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("continuation admission: %#v", outcome)
	}
	fillSendQueue(t, session)

	// Completing the active response wakes the independent dispatcher.
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-active","status":"failed"}}`),
	})
	waitForResponseState(t, ctx, session, "continuation popped into dispatch", func(st *responseState) bool {
		return st.inflight != nil && len(st.pending) == 0
	})

	cancelDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		cancelDone <- session.SendWithOutcome(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeResponseCancel,
			Value: messages.NewResponseCancelValue(),
		})
	}()
	select {
	case outcome := <-cancelDone:
		t.Fatalf("response.cancel completed while popped continuation held admission: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}

	var wire []models.SessionEventType
	for len(wire) < 3 {
		event, ok := session.SendQueue().ReadBlockingContext(ctx)
		if !ok {
			t.Fatalf("send queue yielded %v, want initial create, continuation and cancel around the seeds", wire)
		}
		if event.Type != models.SessionEventInputAudioBufferAppend {
			wire = append(wire, event.Type)
		}
	}
	if wire[0] != wireResponseCreate || wire[1] != wireResponseCreate || wire[2] != wireResponseCancel {
		t.Fatalf("wire order = %v, want initial response.create, popped continuation, response.cancel", wire)
	}
	select {
	case outcome := <-cancelDone:
		if !outcome.OK() {
			t.Fatalf("response.cancel admission: %#v", outcome)
		}
	case <-ctx.Done():
		t.Fatal("response.cancel remained blocked after continuation admission")
	}
}

func TestRealtimeSession_DispatchFailureInvalidatesBeforeFreshAdmission(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	fillSendQueue(t, session)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-active"}}`),
	})
	if outcome := session.RequestResponse(ctx); !outcome.OK() {
		t.Fatalf("queue continuation: %#v", outcome)
	}
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-active","status":"failed"}}`),
	})
	// The send queue's drop observer runs inside the failing enqueue, while
	// the dispatcher holds the response wire lock and before it invalidates
	// the failed generation.
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	session.SendQueue().SetOnDrop(func(models.SessionEvent) {
		enterOnce.Do(func() { close(entered) })
		<-release
	})
	dispatchDone := make(chan struct{})
	go func() {
		session.dispatchPendingResponseIntents(ctx)
		close(dispatchDone)
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("failed dispatch did not reach the full send queue")
	}

	freshDone := make(chan messages.SessionSendOutcome, 1)
	go func() { freshDone <- session.RequestResponse(ctx) }()
	select {
	case outcome := <-freshDone:
		t.Fatalf("fresh response admitted before failed generation invalidation: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := session.SendQueue().Read(); !ok {
		t.Fatal("failed to free one send queue slot")
	}
	close(release)
	select {
	case <-dispatchDone:
	case <-ctx.Done():
		t.Fatal("failed dispatch did not finish")
	}
	select {
	case outcome := <-freshDone:
		if !outcome.OK() {
			t.Fatalf("fresh response after failed generation: %#v", outcome)
		}
	case <-ctx.Done():
		t.Fatal("fresh response remained blocked after failed generation cleanup")
	}
	if !realtimeResponseActive(session) {
		t.Fatal("fresh response lost its slot to the failed generation's cleanup")
	}
}

// waitForResponseState polls the response state under its lock until cond
// holds.
func waitForResponseState(t *testing.T, ctx context.Context, session *realtimeSession, phase string, cond func(*responseState) bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		session.responseMu.Lock()
		ok := cond(&session.response)
		session.responseMu.Unlock()
		if ok {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", phase)
		}
	}
}

// A reserving response.create that fails local admission must give back the
// slot and its single retry: nothing reached the provider to retry.
func TestRealtimeSession_FailedDirectDispatchForgetsRetryAndSlot(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	fillSendQueue(t, session)
	if outcome := session.RequestResponse(t.Context()); outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("response admission on a full queue = %#v, want buffer full", outcome)
	}
	session.responseMu.Lock()
	st := session.response
	session.responseMu.Unlock()
	if st.slot != responseSlotIdle || st.retry != retryNone || st.inflight != nil {
		t.Fatalf("state after failed dispatch: slot=%d retry=%d inflight=%v", st.slot, st.retry, st.inflight)
	}
}

func settledOutcome(outcome messages.SessionSendOutcome) <-chan messages.SessionSendOutcome {
	ch := make(chan messages.SessionSendOutcome, 1)
	ch <- outcome
	return ch
}

func TestRealtimeSession_WaitForAudioIntentSettlements(t *testing.T) {
	failure := errors.New("wire rejected")
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ok := messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	if err := session.waitForAudioIntentSettlements(t.Context(), []<-chan messages.SessionSendOutcome{settledOutcome(ok), settledOutcome(ok)}); err != nil {
		t.Fatalf("settled intents: %v", err)
	}
	err := session.waitForAudioIntentSettlements(t.Context(), []<-chan messages.SessionSendOutcome{
		settledOutcome(messages.SessionSendOutcome{Status: messages.SessionSendClosed, Err: failure}),
	})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), string(messages.SessionSendClosed)) {
		t.Fatalf("failed intent error = %v", err)
	}
	err = session.waitForAudioIntentSettlements(t.Context(), []<-chan messages.SessionSendOutcome{
		settledOutcome(messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}),
	})
	if err == nil || !strings.Contains(err.Error(), string(messages.SessionSendBufferFull)) {
		t.Fatalf("failed intent without cause = %v", err)
	}

	pending := []<-chan messages.SessionSendOutcome{make(chan messages.SessionSendOutcome)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := session.waitForAudioIntentSettlements(ctx, pending); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	closeForTest(t, session)
	if err := session.waitForAudioIntentSettlements(t.Context(), pending); err == nil || !strings.Contains(err.Error(), "closed before deferred audio intent settled") {
		t.Fatalf("wait after close = %v", err)
	}
	session.SetTerminalError(failure)
	if err := session.waitForAudioIntentSettlements(t.Context(), pending); !errors.Is(err, failure) {
		t.Fatalf("wait after terminal failure = %v", err)
	}
}

func TestDeferredAudioIntentError(t *testing.T) {
	if err := deferredAudioIntentError(messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}); err != nil {
		t.Fatalf("success = %v", err)
	}
	cause := errors.New("cause")
	if err := deferredAudioIntentError(messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: cause}); !errors.Is(err, cause) {
		t.Fatalf("wrapped = %v", err)
	}
}

// The default dialer is the shared live dialer.
func TestNewDefaultWebSocketDialer(t *testing.T) {
	if NewDefaultWebSocketDialer() == nil {
		t.Fatal("nil dialer")
	}
}
