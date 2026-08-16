package logging

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var updateCrossingGolden = flag.Bool("update-crossing-golden", false, "update typed-buffer crossing golden output")

// The golden is embedded so ordinary test runs only compare with committed
// output. The explicit update flag is the only workflow that writes it.
//
//go:embed testdata/crossing.jsonl
var crossingFixtures embed.FS

type capturedLog struct {
	level   string
	message string
	fields  []Field
}

type captureLogger struct {
	mu      sync.Mutex
	entries []capturedLog
}

func (l *captureLogger) record(level, message string, fields ...Field) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cloned := append([]Field(nil), fields...)
	l.entries = append(l.entries, capturedLog{level: level, message: message, fields: cloned})
}

func (l *captureLogger) Debug(message string, fields ...Field) {
	l.record("debug", message, fields...)
}

func (l *captureLogger) Info(message string, fields ...Field) {
	l.record("info", message, fields...)
}

func (l *captureLogger) Warn(message string, fields ...Field) {
	l.record("warn", message, fields...)
}

func (l *captureLogger) Error(message string, fields ...Field) {
	l.record("error", message, fields...)
}

func (l *captureLogger) Fatal(message string, fields ...Field) {
	l.record("fatal", message, fields...)
}

func (l *captureLogger) Panic(message string, fields ...Field) {
	l.record("panic", message, fields...)
}

func (l *captureLogger) snapshot() []capturedLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries := make([]capturedLog, len(l.entries))
	copy(entries, l.entries)
	return entries
}

type canonicalCrossingLine struct {
	Message     string            `json:"message"`
	Direction   CrossingDirection `json:"direction"`
	Buffer      string            `json:"buffer"`
	MessageType string            `json:"type"`
	Modality    CrossingModality  `json:"modality"`
	ByteSize    int               `json:"byte_size"`
	Sequence    uint64            `json:"sequence"`
	LogicalTick uint64            `json:"logical_tick"`
}

func TestCrossingEmitterS3GoldenScenario(t *testing.T) {
	logger := &captureLogger{}
	emitter := NewCrossingEmitter(logger)
	events := []CrossingEvent{
		{Direction: CrossingDirectionIn, Buffer: "model.inbox", MessageType: "InferenceRequest", Modality: CrossingModalityText, ByteSize: 128, LogicalTick: 4},
		{Direction: CrossingDirectionOut, Buffer: "model.delta_outbox", MessageType: "StreamMessage", Modality: CrossingModalityText, ByteSize: 42, LogicalTick: 4},
		{Direction: CrossingDirectionIn, Buffer: "tool.inbox", MessageType: "ToolBatchRequest", Modality: CrossingModalityTool, ByteSize: 256, LogicalTick: 5},
		{Direction: CrossingDirectionOut, Buffer: "tool.delta_outbox", MessageType: "StreamMessage", Modality: CrossingModalityAudio, ByteSize: 640, LogicalTick: 5},
		{Direction: CrossingDirectionIn, Buffer: "user.inbox", MessageType: "UserRequest", Modality: CrossingModalityText, ByteSize: 7, LogicalTick: 6},
		{Direction: CrossingDirectionOut, Buffer: "user.outbox", MessageType: "UserResponse", Modality: CrossingModalityText, ByteSize: 18, LogicalTick: 6},
		{Direction: CrossingDirectionIn, Buffer: "kernel.delta_inbox", MessageType: "KernelDeltaRequest", Modality: CrossingModalityData, ByteSize: 1024, LogicalTick: 7},
	}

	for index, want := range events {
		got, err := emitter.Emit(want)
		if err != nil {
			t.Fatalf("event %d: Emit: %v", index, err)
		}
		want.Sequence = uint64(index + 1)
		if got != want {
			t.Fatalf("event %d: got %+v, want %+v", index, got, want)
		}
	}

	entries := logger.snapshot()
	if len(entries) != len(events) {
		t.Fatalf("log count = %d, want %d", len(entries), len(events))
	}
	var encoded bytes.Buffer
	for index, entry := range entries {
		if entry.level != "info" || entry.message != CrossingLogMessage {
			t.Fatalf("record %d = %#v, want one info %q record", index, entry, CrossingLogMessage)
		}
		got, err := eventFromFields(entry.fields)
		if err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
		want := events[index]
		want.Sequence = uint64(index + 1)
		if got != want {
			t.Fatalf("record %d fields = %+v, want %+v", index, got, want)
		}
		line, err := json.Marshal(canonicalCrossingLine{
			Message: CrossingLogMessage, Direction: got.Direction, Buffer: got.Buffer,
			MessageType: got.MessageType, Modality: got.Modality, ByteSize: got.ByteSize,
			Sequence: got.Sequence, LogicalTick: got.LogicalTick,
		})
		if err != nil {
			t.Fatalf("record %d: marshal golden: %v", index, err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	assertCrossingGolden(t, encoded.Bytes())
}

func TestCrossingEmitterNeverLogsAudioPayload(t *testing.T) {
	const recognizableAudio = "AUDIO-PAYLOAD-DO-NOT-LOG-00010203"
	payload := []byte(recognizableAudio)
	logger := &captureLogger{}
	emitter := NewCrossingEmitter(logger)

	got, err := emitter.Emit(CrossingEvent{
		Direction: CrossingDirectionIn, Buffer: "audio.inbox", MessageType: "AudioFrame",
		Modality: CrossingModalityAudio, ByteSize: len(payload), LogicalTick: 19,
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if got.ByteSize != len(payload) {
		t.Fatalf("byte size = %d, want %d", got.ByteSize, len(payload))
	}

	entries := logger.snapshot()
	if len(entries) != 1 {
		t.Fatalf("log count = %d, want 1", len(entries))
	}
	for _, entry := range entries {
		if entry.level != "info" {
			t.Fatalf("unexpected log level %q", entry.level)
		}
		for _, field := range entry.fields {
			if _, ok := field.Value.([]byte); ok {
				t.Fatalf("field %q carried a byte payload", field.Key)
			}
		}
	}
	encoded := canonicalLogBytes(t, entries)
	if bytes.Contains(encoded, payload) {
		t.Fatalf("captured logs contain recognizable audio payload: %q", payload)
	}
	if !bytes.Contains(encoded, []byte(strconv.Itoa(len(payload)))) {
		t.Fatalf("captured logs do not contain exact byte size %d: %s", len(payload), encoded)
	}
}

func TestCrossingEmitterRejectsInvalidMetadataWithoutLogging(t *testing.T) {
	valid := CrossingEvent{
		Direction: CrossingDirectionIn, Buffer: "model.inbox", MessageType: "InferenceRequest",
		Modality: CrossingModalityText, ByteSize: 1, LogicalTick: 1,
	}
	tests := []struct {
		name   string
		mutate func(*CrossingEvent)
	}{
		{name: "direction", mutate: func(event *CrossingEvent) { event.Direction = "sideways" }},
		{name: "buffer", mutate: func(event *CrossingEvent) { event.Buffer = " " }},
		{name: "message type", mutate: func(event *CrossingEvent) { event.MessageType = "" }},
		{name: "modality", mutate: func(event *CrossingEvent) { event.Modality = "\n" }},
		{name: "byte size", mutate: func(event *CrossingEvent) { event.ByteSize = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger := &captureLogger{}
			emitter := NewCrossingEmitter(logger)
			event := valid
			test.mutate(&event)
			if _, err := emitter.Emit(event); !errors.Is(err, ErrInvalidCrossingEvent) {
				t.Fatalf("Emit error = %v, want ErrInvalidCrossingEvent", err)
			}
			if got := len(logger.snapshot()); got != 0 {
				t.Fatalf("log count = %d, want 0", got)
			}
			if got := emitter.LastSequence(); got != 0 {
				t.Fatalf("last sequence = %d, want 0", got)
			}
		})
	}
}

func TestCrossingEmitterValidatesExplicitMonotonicSequence(t *testing.T) {
	logger := &captureLogger{}
	emitter := NewCrossingEmitter(logger)
	event := CrossingEvent{
		Direction: CrossingDirectionOut, Buffer: "model.outbox", MessageType: "StreamMessage",
		Modality: CrossingModalityText, ByteSize: 3, LogicalTick: 2,
	}
	got, err := emitter.Emit(CrossingEvent{Direction: event.Direction, Buffer: event.Buffer, MessageType: event.MessageType, Modality: event.Modality, ByteSize: event.ByteSize, Sequence: 4, LogicalTick: event.LogicalTick})
	if err != nil || got.Sequence != 4 {
		t.Fatalf("explicit sequence result = %+v, err %v", got, err)
	}
	got, err = emitter.Emit(event)
	if err != nil || got.Sequence != 5 {
		t.Fatalf("auto sequence result = %+v, err %v", got, err)
	}
	if _, err := emitter.Emit(CrossingEvent{Direction: event.Direction, Buffer: event.Buffer, MessageType: event.MessageType, Modality: event.Modality, ByteSize: event.ByteSize, Sequence: 5, LogicalTick: event.LogicalTick}); !errors.Is(err, ErrInvalidCrossingEvent) {
		t.Fatalf("duplicate sequence error = %v, want ErrInvalidCrossingEvent", err)
	}
	if got := len(logger.snapshot()); got != 2 {
		t.Fatalf("log count = %d, want 2", got)
	}
}

type lineLogger struct {
	data bytes.Buffer
}

func (l *lineLogger) Debug(string, ...Field) {}

func (l *lineLogger) Info(message string, fields ...Field) {
	event, err := eventFromFields(fields)
	if err != nil {
		return
	}
	line, err := json.Marshal(canonicalCrossingLine{
		Message: message, Direction: event.Direction, Buffer: event.Buffer,
		MessageType: event.MessageType, Modality: event.Modality, ByteSize: event.ByteSize,
		Sequence: event.Sequence, LogicalTick: event.LogicalTick,
	})
	if err != nil {
		return
	}
	l.data.Write(line)
	l.data.WriteByte('\n')
}

func (l *lineLogger) Warn(string, ...Field)  {}
func (l *lineLogger) Error(string, ...Field) {}
func (l *lineLogger) Fatal(string, ...Field) {}
func (l *lineLogger) Panic(string, ...Field) {}

func TestCrossingEmitterConcurrentRecordsAreCompleteAndMonotonic(t *testing.T) {
	const (
		producerCount     = 12
		eventsPerProducer = 75
	)
	logger := &lineLogger{}
	emitter := NewCrossingEmitter(logger)
	start := make(chan struct{})
	var wait sync.WaitGroup
	errCh := make(chan error, producerCount*eventsPerProducer)
	wait.Add(producerCount)
	for producer := 0; producer < producerCount; producer++ {
		go func(producer int) {
			defer wait.Done()
			<-start
			direction := CrossingDirectionIn
			if producer%2 != 0 {
				direction = CrossingDirectionOut
			}
			for eventIndex := 0; eventIndex < eventsPerProducer; eventIndex++ {
				_, err := emitter.Emit(CrossingEvent{
					Direction: direction,
					Buffer:    fmt.Sprintf("producer.%d", producer), MessageType: "StreamMessage",
					Modality: CrossingModalityData, ByteSize: producer + eventIndex,
					LogicalTick: uint64(eventIndex),
				})
				if err != nil {
					errCh <- err
				}
			}
		}(producer)
	}
	close(start)
	wait.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Emit: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(logger.data.String()), "\n")
	total := producerCount * eventsPerProducer
	if len(lines) != total {
		t.Fatalf("line count = %d, want %d", len(lines), total)
	}
	for index, line := range lines {
		var record canonicalCrossingLine
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not one parseable record: %v; line=%q", index, err, line)
		}
		if record.Message != CrossingLogMessage || record.MessageType != "StreamMessage" || record.Modality != CrossingModalityData {
			t.Fatalf("line %d metadata = %+v, want canonical crossing metadata", index, record)
		}
		if record.Sequence != uint64(index+1) {
			t.Fatalf("line %d sequence = %d, want monotonic sequence %d", index, record.Sequence, index+1)
		}
	}
}

func eventFromFields(fields []Field) (CrossingEvent, error) {
	wantKeys := []string{
		CrossingFieldDirection, CrossingFieldBuffer, CrossingFieldMessageType,
		CrossingFieldModality, CrossingFieldByteSize, CrossingFieldSequence,
		CrossingFieldLogicalTick,
	}
	if len(fields) != len(wantKeys) {
		return CrossingEvent{}, fmt.Errorf("field count = %d, want %d", len(fields), len(wantKeys))
	}
	var event CrossingEvent
	for index, field := range fields {
		if field.Key != wantKeys[index] {
			return CrossingEvent{}, fmt.Errorf("field %d key = %q, want %q", index, field.Key, wantKeys[index])
		}
		switch field.Key {
		case CrossingFieldDirection:
			value, ok := field.Value.(CrossingDirection)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("direction field type = %T", field.Value)
			}
			event.Direction = value
		case CrossingFieldBuffer:
			value, ok := field.Value.(string)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("buffer field type = %T", field.Value)
			}
			event.Buffer = value
		case CrossingFieldMessageType:
			value, ok := field.Value.(string)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("message type field type = %T", field.Value)
			}
			event.MessageType = value
		case CrossingFieldModality:
			value, ok := field.Value.(CrossingModality)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("modality field type = %T", field.Value)
			}
			event.Modality = value
		case CrossingFieldByteSize:
			value, ok := field.Value.(int)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("byte size field type = %T", field.Value)
			}
			event.ByteSize = value
		case CrossingFieldSequence:
			value, ok := field.Value.(uint64)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("sequence field type = %T", field.Value)
			}
			event.Sequence = value
		case CrossingFieldLogicalTick:
			value, ok := field.Value.(uint64)
			if !ok {
				return CrossingEvent{}, fmt.Errorf("logical tick field type = %T", field.Value)
			}
			event.LogicalTick = value
		}
	}
	return event, nil
}

func canonicalLogBytes(t *testing.T, entries []capturedLog) []byte {
	t.Helper()
	var encoded bytes.Buffer
	for index, entry := range entries {
		if entry.message != CrossingLogMessage {
			t.Fatalf("record %d message = %q, want %q", index, entry.message, CrossingLogMessage)
		}
		event, err := eventFromFields(entry.fields)
		if err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
		line, err := json.Marshal(canonicalCrossingLine{
			Message: entry.message, Direction: event.Direction, Buffer: event.Buffer,
			MessageType: event.MessageType, Modality: event.Modality, ByteSize: event.ByteSize,
			Sequence: event.Sequence, LogicalTick: event.LogicalTick,
		})
		if err != nil {
			t.Fatalf("record %d: marshal: %v", index, err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	return encoded.Bytes()
}

func assertCrossingGolden(t *testing.T, got []byte) {
	t.Helper()
	path := filepath.FromSlash("testdata/crossing.jsonl")
	if *updateCrossingGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write crossing golden: %v", err)
		}
		return
	}
	want, err := crossingFixtures.ReadFile(filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("read crossing golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("crossing golden differs; run with -update-crossing-golden only after reviewing the format change\ngot:\n%s\nwant:\n%s", got, want)
	}
}
