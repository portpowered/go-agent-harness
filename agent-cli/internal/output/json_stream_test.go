package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestWriteEventStreamToWriter_RefusalNotWrittenToWriter(t *testing.T) {
	// REFUSAL events should be routed to errW, NOT to the main writer.
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello ")},
		{Type: messages.StreamTypeRefusal, Value: messages.NewRefusalValue("I cannot assist with that.")},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("world")},
	}

	var buf, errBuf bytes.Buffer
	err := WriteEventStreamToWriter(&buf, &errBuf, newMockStream(events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := buf.String()
	if got != "hello world" {
		t.Errorf("expected %q, got %q", "hello world", got)
	}
	if strings.Contains(got, "REFUSAL") || strings.Contains(got, "cannot assist") {
		t.Error("refusal text should not appear in the writer output")
	}

	// Verify refusal was written to errW.
	if !strings.Contains(errBuf.String(), "[REFUSAL]") {
		t.Errorf("expected errW to contain [REFUSAL], got %q", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "I cannot assist with that.") {
		t.Errorf("expected errW to contain refusal text, got %q", errBuf.String())
	}
}

func TestWriteEventStreamToWriter_RefusalOnlyStream(t *testing.T) {
	// A stream containing only a refusal should produce no output on the writer.
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeRefusal, Value: messages.NewRefusalValue("I cannot assist with that.")},
	}

	var buf, errBuf bytes.Buffer
	err := WriteEventStreamToWriter(&buf, &errBuf, newMockStream(events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output on writer for refusal-only stream, got %q", buf.String())
	}
	if !strings.Contains(errBuf.String(), "[REFUSAL]") {
		t.Errorf("expected errW to contain [REFUSAL], got %q", errBuf.String())
	}
}

func TestWriteEventStreamToWriter_EmptyRefusalIgnored(t *testing.T) {
	// An empty refusal value should be silently ignored.
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("ok")},
		{Type: messages.StreamTypeRefusal, Value: messages.NewRefusalValue("")},
	}

	var buf, errBuf bytes.Buffer
	err := WriteEventStreamToWriter(&buf, &errBuf, newMockStream(events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "ok" {
		t.Errorf("expected %q, got %q", "ok", buf.String())
	}
	if errBuf.Len() != 0 {
		t.Errorf("expected no errW output for empty refusal, got %q", errBuf.String())
	}
}

func TestWriteStreamEventJSON_RefusalEvent(t *testing.T) {
	// Verify REFUSAL events are serializable as NDJSON (used for auditing/debugging).
	msg := messages.StreamMessage{
		Type:               messages.StreamTypeRefusal,
		ActorProvidedIndex: 0,
		Value:              messages.NewRefusalValue("I cannot assist with that."),
	}

	var buf bytes.Buffer
	err := WriteStreamEventJSON(&buf, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var evt streamEventJSON
	if err := json.Unmarshal(buf.Bytes(), &evt); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if evt.Type != "REFUSAL" {
		t.Errorf("expected type %q, got %q", "REFUSAL", evt.Type)
	}

	// The value should contain the refusal message.
	var val messages.RefusalValue
	if err := json.Unmarshal(evt.Value, &val); err != nil {
		t.Fatalf("failed to unmarshal value: %v", err)
	}
	if val.Message != "I cannot assist with that." {
		t.Errorf("expected message %q, got %q", "I cannot assist with that.", val.Message)
	}
}

func TestWriteStreamEventJSON_RefusalNotMixedWithTextEvents(t *testing.T) {
	// Verify that text and refusal events produce separate NDJSON lines with distinct types.
	events := []messages.StreamMessage{
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("hello")},
		{Type: messages.StreamTypeRefusal, Value: messages.NewRefusalValue("Refused.")},
	}

	var buf bytes.Buffer
	for _, evt := range events {
		if err := WriteStreamEventJSON(&buf, evt); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d", len(lines))
	}

	var textEvt, refusalEvt streamEventJSON
	if err := json.Unmarshal([]byte(lines[0]), &textEvt); err != nil {
		t.Fatalf("failed to unmarshal text event: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &refusalEvt); err != nil {
		t.Fatalf("failed to unmarshal refusal event: %v", err)
	}

	if textEvt.Type != "TEXT.DELTA" {
		t.Errorf("expected first event type %q, got %q", "TEXT.DELTA", textEvt.Type)
	}
	if refusalEvt.Type != "REFUSAL" {
		t.Errorf("expected second event type %q, got %q", "REFUSAL", refusalEvt.Type)
	}
}
