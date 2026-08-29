package messages

import (
	"encoding/json"
	"testing"
)

func TestTerminalMetadataSerializesOnMessageEnd(t *testing.T) {
	value := NewMessageEndValueWithTerminal(
		TokenUsage{},
		TerminalReasonProviderAuthoredCompletion,
		TerminalProvenanceProvider,
		TerminalOutputComplete,
	)

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["terminal_reason"] != string(TerminalReasonProviderAuthoredCompletion) {
		t.Fatalf("terminal_reason: got %v", got["terminal_reason"])
	}
	if got["terminal_provenance"] != string(TerminalProvenanceProvider) {
		t.Fatalf("terminal_provenance: got %v", got["terminal_provenance"])
	}
	if got["output_state"] != string(TerminalOutputComplete) {
		t.Fatalf("output_state: got %v", got["output_state"])
	}
}

func TestTerminalMetadataSerializesOnErrorValue(t *testing.T) {
	value := NewErrorValueWithTerminal(
		"request canceled",
		"cancellation",
		TerminalReasonCancellation,
		TerminalProvenanceGateway,
		TerminalOutputPartial,
	)

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["message"] != "request canceled" {
		t.Fatalf("message: got %v", got["message"])
	}
	if got["classification"] != "cancellation" {
		t.Fatalf("classification: got %v", got["classification"])
	}
	if got["terminal_reason"] != string(TerminalReasonCancellation) {
		t.Fatalf("terminal_reason: got %v", got["terminal_reason"])
	}
	if got["terminal_provenance"] != string(TerminalProvenanceGateway) {
		t.Fatalf("terminal_provenance: got %v", got["terminal_provenance"])
	}
	if got["output_state"] != string(TerminalOutputPartial) {
		t.Fatalf("output_state: got %v", got["output_state"])
	}
}

func TestNonTerminalErrorValuePreservesDiagnosticSemantics(t *testing.T) {
	value := NewNonTerminalErrorValueWithDetails(
		"response is not active",
		"invalid_request_error",
		"response_cancel_not_active",
		"response.cancel",
		"evt-123",
	)
	value.Classification = "response_cancel_not_active"

	if value.IsTerminal() || !value.IsNonTerminal() {
		t.Fatal("nonterminal error value must not be terminal")
	}
	if NewErrorValue("legacy error").IsNonTerminal() {
		t.Fatal("legacy error values must remain terminal by default")
	}

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for key, want := range map[string]any{
		"message":        "response is not active",
		"classification": "response_cancel_not_active",
		"non_terminal":   true,
		"error_type":     "invalid_request_error",
		"code":           "response_cancel_not_active",
		"param":          "response.cancel",
		"event_id":       "evt-123",
	} {
		if got[key] != want {
			t.Fatalf("%s: got %v, want %v", key, got[key], want)
		}
	}
	if _, ok := got["terminal_reason"]; ok {
		t.Fatalf("nonterminal diagnostic unexpectedly has terminal_reason: %v", got["terminal_reason"])
	}
}

func TestTerminalMetadataSerializesOnSessionClose(t *testing.T) {
	value := NewSessionCloseValueWithTerminal(
		"session-1",
		"transport_closed",
		"provider_close",
		TerminalReasonProviderClose,
		TerminalProvenanceSession,
		TerminalOutputNotApplicable,
	)

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["reason"] != "transport_closed" {
		t.Fatalf("reason: got %v", got["reason"])
	}
	if got["classification"] != "provider_close" {
		t.Fatalf("classification: got %v", got["classification"])
	}
	if got["terminal_reason"] != string(TerminalReasonProviderClose) {
		t.Fatalf("terminal_reason: got %v", got["terminal_reason"])
	}
}

func TestLegacyTerminalConstructorsOmitAdditiveFields(t *testing.T) {
	values := []StreamMessageValue{
		NewMessageEndValue(TokenUsage{}),
		NewErrorValue("failed"),
		NewSessionCloseValue("session-1", "client_close"),
	}

	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal(%T): %v", value, err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%T): %v", value, err)
		}
		if _, ok := got["terminal_reason"]; ok {
			t.Fatalf("%T unexpectedly serialized terminal_reason: %s", value, data)
		}
		if _, ok := got["terminal_provenance"]; ok {
			t.Fatalf("%T unexpectedly serialized terminal_provenance: %s", value, data)
		}
		if _, ok := got["output_state"]; ok {
			t.Fatalf("%T unexpectedly serialized output_state: %s", value, data)
		}
	}
}
