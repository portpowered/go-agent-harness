package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestRealtimeOutboundEvents_ResponseCancelMapsToProviderEvent(t *testing.T) {
	events, ok := realtimeOutboundEvents(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	})
	if !ok {
		t.Fatal("RESPONSE.CANCEL was not accepted as an outbound event")
	}
	if len(events) != 1 {
		t.Fatalf("provider event count = %d, want 1", len(events))
	}
	if events[0].Type != models.SessionEventResponseCancel {
		t.Fatalf("provider event type = %q, want %q", events[0].Type, models.SessionEventResponseCancel)
	}
}

func TestRealtimeOutboundEvents_ToolAcknowledgementCarriesInstructions(t *testing.T) {
	events, ok := realtimeOutboundEvents(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewToolAcknowledgementResponseCreateValue(),
	})
	if !ok || len(events) != 1 {
		t.Fatalf("acknowledgement events = %#v, ok=%t; want one event", events, ok)
	}
	if events[0].Type != models.SessionEventResponseCreate {
		t.Fatalf("event type = %q, want %q", events[0].Type, models.SessionEventResponseCreate)
	}
	var payload struct {
		Response struct {
			Instructions string `json:"instructions"`
		} `json:"response"`
	}
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatalf("acknowledgement payload: %v", err)
	}
	if payload.Response.Instructions != messages.ToolAcknowledgementInstructions {
		t.Fatalf("acknowledgement instructions = %q, want %q", payload.Response.Instructions, messages.ToolAcknowledgementInstructions)
	}
}

func TestRealtimeInboundMessages_InactiveCancelRejectionIsNonTerminalDiagnostic(t *testing.T) {
	raw := json.RawMessage(`{"type":"error","error":{"type":"invalid_request_error","code":"response_cancel_not_active","param":"response.cancel","event_id":"evt-cancel-1","message":"Can only cancel an active response."}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventError, Data: raw})
	if len(got) != 1 || got[0].Type != messages.StreamTypeError {
		t.Fatalf("error event normalization = %#v, want one ERROR", got)
	}
	value, ok := got[0].Value.(*messages.ErrorValue)
	if !ok || value == nil {
		t.Fatalf("normalized error value = %T, want *messages.ErrorValue", got[0].Value)
	}
	if value.IsTerminal() || !value.IsNonTerminal() {
		t.Fatalf("inactive-cancel diagnostic terminal state: %#v", value)
	}
	if value.Classification != providers.ErrorClassResponseCancelNotActive ||
		value.Message != "Can only cancel an active response." ||
		value.ErrorType != "invalid_request_error" ||
		value.Code != "response_cancel_not_active" ||
		value.Param != "response.cancel" ||
		value.EventID != "evt-cancel-1" {
		t.Fatalf("inactive-cancel diagnostic details: %#v", value)
	}
	if value.TerminalReason != "" || value.TerminalProvenance != "" || value.OutputState != "" {
		t.Fatalf("inactive-cancel diagnostic unexpectedly has terminal metadata: %#v", value)
	}
}

func TestRealtimeInboundMessages_InactiveCancelHandlingRequiresExactProviderFields(t *testing.T) {
	cases := []struct {
		name        string
		errorType   string
		code        string
		nonTerminal bool
	}{
		{name: "exact", errorType: "invalid_request_error", code: "response_cancel_not_active", nonTerminal: true},
		{name: "different code", errorType: "invalid_request_error", code: "response_cancel_not_active_other"},
		{name: "different type", errorType: "server_error", code: "response_cancel_not_active"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    tc.errorType,
					"code":    tc.code,
					"message": "provider error",
				},
			})
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventError, Data: data})
			value, ok := got[0].Value.(*messages.ErrorValue)
			if !ok || value == nil {
				t.Fatalf("normalized error value = %T, want *messages.ErrorValue", got[0].Value)
			}
			if value.IsNonTerminal() != tc.nonTerminal {
				t.Fatalf("nonterminal = %t, want %t; value = %#v", value.IsNonTerminal(), tc.nonTerminal, value)
			}
			if !tc.nonTerminal && (value.Classification != providers.ErrorClassProviderRejected || value.TerminalReason != messages.TerminalReasonTerminalFailure) {
				t.Fatalf("negative control metadata = %#v", value)
			}
		})
	}
}

func TestRealtimeInboundMessages_ActiveResponseCreateRejectionIsNonTerminal(t *testing.T) {
	raw := json.RawMessage(`{"type":"error","error":{"type":"invalid_request_error","code":"conversation_already_has_active_response","message":"Conversation already has an active response."}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventError, Data: raw})
	if len(got) != 1 {
		t.Fatalf("active-response error normalization = %#v, want one event", got)
	}
	value, ok := got[0].Value.(*messages.ErrorValue)
	if !ok || value == nil {
		t.Fatalf("active-response error value = %T, want *messages.ErrorValue", got[0].Value)
	}
	if !value.IsNonTerminal() || value.Classification != realtimeResponseCreateActiveClass || value.Code != realtimeResponseCreateActiveCode {
		t.Fatalf("active-response error metadata = %#v, want nonterminal recoverable diagnostic", value)
	}
}

func TestRealtimeInboundMessagesResponseDonePreservesFailureStatus(t *testing.T) {
	raw := json.RawMessage(`{"type":"response.done","response":{"status":"failed","status_details":{"reason":"max_output_tokens","error":{"code":"too_many_tokens","message":"response exceeded the model limit"}}}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventResponseDone, Data: raw})
	if len(got) != 1 {
		t.Fatalf("response.done messages = %d, want one", len(got))
	}
	value, ok := got[0].Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		t.Fatalf("response.done value = %#v, want *MessageEndValue", got[0].Value)
	}
	if value.Status != "failed" {
		t.Fatalf("response.done status = %q, want failed", value.Status)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("response.done terminal reason = %q, want terminal_failure", value.TerminalReason)
	}
	if !strings.Contains(value.StatusDetails, "reason=max_output_tokens") || !strings.Contains(value.StatusDetails, "message=response exceeded the model limit") {
		t.Fatalf("response.done details = %q, want bounded reason and message", value.StatusDetails)
	}
	if value.ProviderErrorCode != "too_many_tokens" || value.ProviderErrorMessage != "response exceeded the model limit" {
		t.Fatalf("response.done provider error metadata = code %q message %q", value.ProviderErrorCode, value.ProviderErrorMessage)
	}
}

func TestRealtimeInboundMessagesResponseDoneBoundsProviderErrorMessage(t *testing.T) {
	message := strings.Repeat("provider detail ", 40)
	raw, err := json.Marshal(map[string]any{
		"type": "response.done",
		"response": map[string]any{
			"status": "FAILED",
			"status_details": map[string]any{
				"error": map[string]any{
					"code":    "rate_limit_exceeded",
					"message": message,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal response.done: %v", err)
	}

	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventResponseDone, Data: raw})
	value, ok := got[0].Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		t.Fatalf("response.done value = %#v, want *MessageEndValue", got[0].Value)
	}
	if value.Status != "failed" {
		t.Fatalf("normalized response status = %q, want failed", value.Status)
	}
	if value.ProviderErrorCode != "rate_limit_exceeded" {
		t.Fatalf("provider error code = %q, want rate_limit_exceeded", value.ProviderErrorCode)
	}
	if len(value.ProviderErrorMessage) > realtimeMaxStatusDetailBytes {
		t.Fatalf("provider error message length = %d, want <= %d", len(value.ProviderErrorMessage), realtimeMaxStatusDetailBytes)
	}
	if strings.Contains(value.ProviderErrorMessage, "secret-token") {
		t.Fatal("provider error message exposed an unrelated credential field")
	}
}

func TestRealtimeInboundMessagesClassifiesCreditBalanceExhaustionAsRateLimited(t *testing.T) {
	raw := json.RawMessage(`{"type":"error","error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"You have no credits remaining."}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventError, Data: raw})
	if len(got) != 1 {
		t.Fatalf("error messages = %d, want one", len(got))
	}
	value, ok := got[0].Value.(*messages.ErrorValue)
	if !ok || value == nil {
		t.Fatalf("error value = %#v, want *messages.ErrorValue", got[0].Value)
	}
	if value.Classification != providers.ErrorClassRateLimited {
		t.Fatalf("classification = %q, want %q", value.Classification, providers.ErrorClassRateLimited)
	}
	if value.Code != "credit_balance_exhausted" || value.ErrorType != "insufficient_quota" {
		t.Fatalf("provider error metadata = type %q code %q", value.ErrorType, value.Code)
	}
}

func TestRealtimeInboundMessagesResponseDonePreservesCompletedStatus(t *testing.T) {
	raw := json.RawMessage(`{"type":"response.done","response":{"status":"completed"}}`)
	got := realtimeInboundMessages(models.SessionEvent{Type: models.SessionEventResponseDone, Data: raw})
	value, ok := got[0].Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		t.Fatalf("response.done value = %#v, want *MessageEndValue", got[0].Value)
	}
	if value.Status != "completed" || value.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion {
		t.Fatalf("completed response metadata = status %q reason %q", value.Status, value.TerminalReason)
	}
}

func TestRealtimeInboundMessages_PreservesResponseID(t *testing.T) {
	tests := []struct {
		name  string
		type_ models.SessionEventType
		raw   string
		want  messages.StreamMessageType
	}{
		{name: "created", type_: models.SessionEventResponseCreated, raw: `{"response":{"id":"resp-created"}}`, want: messages.StreamTypeMessageStart},
		{name: "audio", type_: models.SessionEventResponseOutputAudioDelta, raw: `{"response_id":"resp-audio","delta":"AQI=","format":"pcm16"}`, want: messages.StreamTypeAudioDelta},
		{name: "tool", type_: models.SessionEventResponseFunctionCallArgumentsDone, raw: `{"response_id":"resp-tool","call_id":"call-1","name":"lookup","arguments":"{}"}`, want: messages.StreamTypeToolCallEnd},
		{name: "done", type_: models.SessionEventResponseDone, raw: `{"response":{"id":"resp-done","status":"completed"}}`, want: messages.StreamTypeMessageEnd},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := realtimeInboundMessages(models.SessionEvent{Type: test.type_, Data: []byte(test.raw)})
			if len(got) != 1 {
				t.Fatalf("normalized messages = %d, want 1", len(got))
			}
			if got[0].Type != test.want {
				t.Fatalf("normalized type = %q, want %q", got[0].Type, test.want)
			}
			if got[0].ResponseID == "" {
				t.Fatalf("normalized response ID is empty: %#v", got[0])
			}
		})
	}
}
