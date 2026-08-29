package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

func TestWriteStreamEventJSON_PreservesResponseID(t *testing.T) {
	msg := messages.StreamMessage{
		Type:       messages.StreamTypeAudioDelta,
		ResponseID: "resp-json",
		Value:      messages.NewAudioDeltaValue([]byte{1, 2}),
	}

	var buf bytes.Buffer
	if err := WriteStreamEventJSON(&buf, msg); err != nil {
		t.Fatalf("WriteStreamEventJSON: %v", err)
	}
	var event struct {
		ResponseID string `json:"responseId"`
	}
	if err := json.Unmarshal(buf.Bytes(), &event); err != nil {
		t.Fatalf("unmarshal stream event: %v", err)
	}
	if event.ResponseID != msg.ResponseID {
		t.Fatalf("response ID = %q, want %q", event.ResponseID, msg.ResponseID)
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

func TestWriteStreamEventJSON_TerminalMetadataFields(t *testing.T) {
	tests := []struct {
		name               string
		msg                messages.StreamMessage
		wantType           string
		wantClassification string
		wantReason         messages.TerminalReason
		wantProvenance     messages.TerminalProvenance
		wantOutputState    messages.TerminalOutputState
	}{
		{
			name: "provider authored completion",
			msg: messages.StreamMessage{
				Type: messages.StreamTypeMessageEnd,
				Value: messages.NewMessageEndValueWithTerminal(
					messages.TokenUsage{},
					messages.TerminalReasonProviderAuthoredCompletion,
					messages.TerminalProvenanceProvider,
					messages.TerminalOutputComplete,
				),
			},
			wantType:        "MESSAGE.END",
			wantReason:      messages.TerminalReasonProviderAuthoredCompletion,
			wantProvenance:  messages.TerminalProvenanceProvider,
			wantOutputState: messages.TerminalOutputComplete,
		},
		{
			name: "terminal failure after partial output",
			msg: messages.StreamMessage{
				Type: messages.StreamTypeError,
				Value: messages.NewErrorValueWithTerminal(
					"provider failed",
					"transport",
					messages.TerminalReasonTerminalFailure,
					messages.TerminalProvenanceProvider,
					messages.TerminalOutputPartial,
				),
			},
			wantType:           "ERROR",
			wantClassification: "transport",
			wantReason:         messages.TerminalReasonTerminalFailure,
			wantProvenance:     messages.TerminalProvenanceProvider,
			wantOutputState:    messages.TerminalOutputPartial,
		},
		{
			name: "cancellation",
			msg: messages.StreamMessage{
				Type: messages.StreamTypeError,
				Value: messages.NewErrorValueWithTerminal(
					"context canceled",
					string(messages.TerminalReasonCancellation),
					messages.TerminalReasonCancellation,
					messages.TerminalProvenanceLoop,
					messages.TerminalOutputPartial,
				),
			},
			wantType:           "ERROR",
			wantClassification: string(messages.TerminalReasonCancellation),
			wantReason:         messages.TerminalReasonCancellation,
			wantProvenance:     messages.TerminalProvenanceLoop,
			wantOutputState:    messages.TerminalOutputPartial,
		},
		{
			name: "provider close",
			msg: messages.StreamMessage{
				Type: messages.StreamTypeMessageEnd,
				Value: messages.NewMessageEndValueWithTerminal(
					messages.TokenUsage{},
					messages.TerminalReasonProviderClose,
					messages.TerminalProvenanceProvider,
					messages.TerminalOutputPartial,
				),
			},
			wantType:        "MESSAGE.END",
			wantReason:      messages.TerminalReasonProviderClose,
			wantProvenance:  messages.TerminalProvenanceProvider,
			wantOutputState: messages.TerminalOutputPartial,
		},
		{
			name: "session close",
			msg: messages.StreamMessage{
				Type: messages.StreamTypeSessionClose,
				Value: messages.NewSessionCloseValueWithTerminal(
					"session-1",
					"client_close",
					string(messages.TerminalReasonSessionClose),
					messages.TerminalReasonSessionClose,
					messages.TerminalProvenanceLoop,
					messages.TerminalOutputNotApplicable,
				),
			},
			wantType:           "SESSION.CLOSE",
			wantClassification: string(messages.TerminalReasonSessionClose),
			wantReason:         messages.TerminalReasonSessionClose,
			wantProvenance:     messages.TerminalProvenanceLoop,
			wantOutputState:    messages.TerminalOutputNotApplicable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteStreamEventJSON(&buf, tt.msg); err != nil {
				t.Fatalf("WriteStreamEventJSON: %v", err)
			}

			var evt streamEventJSON
			if err := json.Unmarshal(buf.Bytes(), &evt); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			if evt.Type != tt.wantType {
				t.Fatalf("event type = %q, want %q", evt.Type, tt.wantType)
			}

			var value map[string]any
			if err := json.Unmarshal(evt.Value, &value); err != nil {
				t.Fatalf("unmarshal value: %v", err)
			}
			if tt.wantClassification != "" && value["classification"] != tt.wantClassification {
				t.Fatalf("classification = %v, want %q", value["classification"], tt.wantClassification)
			}
			if value["terminal_reason"] != string(tt.wantReason) {
				t.Fatalf("terminal_reason = %v, want %q", value["terminal_reason"], tt.wantReason)
			}
			if value["terminal_provenance"] != string(tt.wantProvenance) {
				t.Fatalf("terminal_provenance = %v, want %q", value["terminal_provenance"], tt.wantProvenance)
			}
			if value["output_state"] != string(tt.wantOutputState) {
				t.Fatalf("output_state = %v, want %q", value["output_state"], tt.wantOutputState)
			}
		})
	}
}
