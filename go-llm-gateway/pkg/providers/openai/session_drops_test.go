package openai

import (
	"context"
	"sync"
	"testing"

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

	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")}
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
