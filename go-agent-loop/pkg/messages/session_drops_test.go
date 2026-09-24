package messages

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestTypedBufferFullDropsNewest(t *testing.T) {
	buffer := NewTypedBuffer[int](1)
	var dropCount atomic.Int64
	buffer.SetOnDrop(func(_ int) {
		dropCount.Add(1)
	})

	first := buffer.WriteContext(context.Background(), 41)
	if first.Status != BufferWriteSucceeded || !first.OK() {
		t.Fatalf("first write returned %+v", first)
	}

	result := make(chan BufferWriteOutcome, 1)
	go func() {
		result <- buffer.WriteContext(context.Background(), 99)
	}()

	var newest BufferWriteOutcome
	select {
	case newest = <-result:
	case <-time.After(time.Second):
		t.Fatal("full-buffer write blocked")
	}
	if newest.Status != BufferWriteBufferFull || newest.OK() || newest.Err != nil {
		t.Fatalf("newest write returned %+v, want buffer_full", newest)
	}
	if got := dropCount.Load(); got != 1 {
		t.Fatalf("drop callback count=%d, want 1", got)
	}
	if buffer.Len() != 1 || !buffer.HasData() {
		t.Fatalf("full buffer state len=%d has_data=%v", buffer.Len(), buffer.HasData())
	}

	retained, ok := buffer.Read()
	if !ok || retained != 41 {
		t.Fatalf("retained value=%d ok=%v, want 41", retained, ok)
	}
	if _, ok := buffer.Read(); ok {
		t.Fatal("newest rejected value was delivered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := buffer.WriteContext(ctx, 123)
	if cancelled.Status != BufferWriteCancelled || !errors.Is(cancelled.Err, context.Canceled) {
		t.Fatalf("cancelled write returned %+v", cancelled)
	}
	if got := dropCount.Load(); got != 1 {
		t.Fatalf("cancelled write changed drop callback count to %d", got)
	}
}
