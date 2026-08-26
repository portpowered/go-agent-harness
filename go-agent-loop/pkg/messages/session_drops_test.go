package messages

import (
	"context"
	"sync"
	"testing"
)

// captureLogger records every Warn call so tests can assert the exact
// structured fields of emitted drop records.
type captureLogger struct {
	mu   sync.Mutex
	warn []captureRecord
}

type captureRecord struct {
	msg    string
	fields map[string]any
}

func (c *captureLogger) Debug(string, ...DropLogField) {}
func (c *captureLogger) Info(string, ...DropLogField)  {}
func (c *captureLogger) Error(string, ...DropLogField) {}
func (c *captureLogger) Fatal(string, ...DropLogField) {}
func (c *captureLogger) Panic(string, ...DropLogField) {}

func (c *captureLogger) Warn(msg string, fields ...DropLogField) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record := captureRecord{msg: msg, fields: make(map[string]any, len(fields))}
	for _, field := range fields {
		record.fields[field.Key] = field.Value
	}
	c.warn = append(c.warn, record)
}

func (c *captureLogger) records() []captureRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captureRecord(nil), c.warn...)
}

func TestAttachDefaultDropObserverEmitsOneLinePerDrop(t *testing.T) {
	logger := &captureLogger{}
	buf := NewTypedBuffer[StreamMessage](1)
	AttachDefaultDropObserver(logger, DropDirectionInput, "session.send_queue", buf,
		func(m StreamMessage) string { return string(m.Type) })

	if !buf.Write(context.Background(), StreamMessage{Type: StreamTypeAudioDelta}) {
		t.Fatal("initial write failed")
	}
	for range 2 {
		if buf.Write(context.Background(), StreamMessage{Type: StreamTypeTextDelta}) {
			t.Fatal("overflow write unexpectedly succeeded")
		}
	}

	records := logger.records()
	if len(records) != 2 {
		t.Fatalf("emitted %d drop records, want exactly one per drop (2)", len(records))
	}
	for i, record := range records {
		if record.msg != DropLogMessage {
			t.Errorf("record %d message = %q, want %q", i, record.msg, DropLogMessage)
		}
		wantCount := int64(i + 1)
		if got := record.fields["count"]; got != wantCount {
			t.Errorf("record %d count = %v, want %d", i, got, wantCount)
		}
		if got := record.fields["direction"]; got != string(DropDirectionInput) {
			t.Errorf("record %d direction = %v, want %q", i, got, DropDirectionInput)
		}
	}
	// The kind reflects the dropped message, not a fixed label.
	// Both drops carry the text-delta kind; the kept audio frame is never
	// reported as dropped.
	if got := records[0].fields["type"]; got != string(StreamTypeTextDelta) {
		t.Errorf("first dropped type = %v, want %q", got, StreamTypeTextDelta)
	}
	if got := records[1].fields["type"]; got != string(StreamTypeTextDelta) {
		t.Errorf("second dropped type = %v, want %q", got, StreamTypeTextDelta)
	}
	if got := records[1].fields["buffer"]; got != "session.send_queue" {
		t.Errorf("record buffer = %v, want session.send_queue", got)
	}
}

func TestAttachDefaultDropObserverSilentWithoutDrops(t *testing.T) {
	logger := &captureLogger{}
	buf := NewTypedBuffer[StreamMessage](8)
	AttachDefaultDropObserver(logger, DropDirectionOutput, "session.receive", buf,
		func(m StreamMessage) string { return string(m.Type) })

	for i := range 8 {
		if !buf.Write(context.Background(), StreamMessage{Type: StreamTypeTextDelta}) {
			t.Fatalf("write %d into an empty buffer failed", i)
		}
	}
	if records := logger.records(); len(records) != 0 {
		t.Fatalf("zero drops emitted %d records, want 0", len(records))
	}
}

func TestAttachDefaultDropObserverNoopOnNil(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	// Nil logger and nil buffer must be safe no-ops.
	AttachDefaultDropObserver[string](nil, DropDirectionInput, "x", buf, nil)
	AttachDefaultDropObserver[string](&captureLogger{}, DropDirectionInput, "x", nil, nil)
	if !buf.Write(context.Background(), "first") {
		t.Fatal("initial write failed")
	}
	buf.Write(context.Background(), "dropped") //nolint:errcheck // deliberate overflow
	if got := buf.Drops(); got != 1 {
		t.Fatalf("Drops() = %d, want 1 even without observer wiring", got)
	}
}
