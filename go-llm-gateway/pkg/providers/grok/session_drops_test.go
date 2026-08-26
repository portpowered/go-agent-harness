package grok

import (
	"context"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// dropCaptureLogger records Warn calls for drop-record assertions.
type dropCaptureLogger struct {
	mu   sync.Mutex
	warn []map[string]any
}

func (c *dropCaptureLogger) Debug(string, ...logging.Field) {}
func (c *dropCaptureLogger) Info(string, ...logging.Field)  {}
func (c *dropCaptureLogger) Error(string, ...logging.Field) {}
func (c *dropCaptureLogger) Fatal(string, ...logging.Field) {}
func (c *dropCaptureLogger) Panic(string, ...logging.Field) {}

func (c *dropCaptureLogger) Warn(msg string, fields ...logging.Field) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record := map[string]any{"msg": msg}
	for _, field := range fields {
		record[field.Key] = field.Value
	}
	c.warn = append(c.warn, record)
}

func (c *dropCaptureLogger) records() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.warn...)
}

func TestGrokSessionDropLoggersForceOverflowBothDirections(t *testing.T) {
	logger := &dropCaptureLogger{}
	conn := newMockConn() // never read; writeLoop is not started
	session := newGrokSession(conn, logger)
	ctx := context.Background()

	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hi")}
	for range 64 {
		if outcome := session.SendWithOutcome(ctx, msg); !outcome.OK() {
			t.Fatalf("queued send returned %+v, want success", outcome)
		}
	}
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
	if record["direction"] != string(messages.DropDirectionInput) {
		t.Errorf("record direction = %v, want input", record["direction"])
	}
	if record["count"] != int64(1) {
		t.Errorf("record count = %v (%T), want int64(1)", record["count"], record["count"])
	}
	// TEXT.DELTA translates to a conversation.item.create wire event before
	// it is queued, so the dropped message kind is the wire event type.
	if record["type"] != "conversation.item.create" {
		t.Errorf("record type = %v, want conversation.item.create", record["type"])
	}

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
	if outRecord["type"] != string(messages.StreamTypeAudioDelta) {
		t.Errorf("second record type = %v, want %q", outRecord["type"], messages.StreamTypeAudioDelta)
	}
}

func TestGrokSessionDropCountersZeroWithoutOverflow(t *testing.T) {
	logger := &dropCaptureLogger{}
	session := newGrokSession(newMockConn(), logger)
	ctx := context.Background()

	msg := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")}
	for range 4 {
		if outcome := session.SendWithOutcome(ctx, msg); !outcome.OK() {
			t.Fatalf("send returned %+v, want success", outcome)
		}
	}
	if session.InputDrops() != 0 || session.OutputDrops() != 0 {
		t.Fatalf("drops = input %d output %d, want 0/0", session.InputDrops(), session.OutputDrops())
	}
	if records := logger.records(); len(records) != 0 {
		t.Fatalf("normal traffic emitted %d drop records, want 0", len(records))
	}
}
