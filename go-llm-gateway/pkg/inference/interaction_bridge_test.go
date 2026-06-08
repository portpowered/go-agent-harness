package inference

import (
	"encoding/json"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

func TestLoopInteractionEventFromGateway(t *testing.T) {
	event := gateway.InteractionEvent{
		InteractionID: "int-1",
		Sequence:      3,
		Type:          gateway.InteractionEventFinalMessage,
		Provider:      "fake",
		Model:         "demo",
		FinalMessage: &gateway.InteractionMessage{
			Role: gateway.InteractionRoleAssistant,
			ContentParts: []gateway.InteractionContent{
				{Type: gateway.InteractionContentText, Text: "done"},
			},
			ToolCalls: []gateway.InteractionToolCall{
				{ID: "tool-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"weather"}`)},
			},
		},
	}

	got := LoopInteractionEventFromGateway(event)

	if got.Type != messages.InteractionEventFinalMessage {
		t.Fatalf("type = %s, want %s", got.Type, messages.InteractionEventFinalMessage)
	}
	if got.FinalMessage == nil {
		t.Fatal("expected final message")
	}
	if text := got.FinalMessage.TextContent(); text != "done" {
		t.Fatalf("final message text = %q, want done", text)
	}
	if len(got.FinalMessage.ToolCalls) != 1 || got.FinalMessage.ToolCalls[0].ID != "tool-1" {
		t.Fatalf("tool calls = %#v", got.FinalMessage.ToolCalls)
	}
}

func TestLoopInteractionEventFromGatewayMapsUsageAndTerminalPayloads(t *testing.T) {
	event := gateway.InteractionEvent{
		InteractionID: "int-1",
		Sequence:      7,
		Type:          gateway.InteractionEventError,
		Usage: &gateway.InteractionUsage{
			InputTokens:  11,
			OutputTokens: 4,
			TotalTokens:  15,
		},
		Error: &gateway.InteractionError{
			Code:           "provider_timeout",
			Message:        "timed out",
			Classification: providers.ErrorClassTransport,
			Retryable:      true,
		},
		Cancellation: &gateway.InteractionCancellation{
			Reason:         "caller_cancelled",
			Message:        "stop requested",
			Classification: providers.ErrorClassCancellation,
			OutputState:    providers.ErrorClassPartialOutput,
		},
	}

	got := LoopInteractionEventFromGateway(event)

	if got.Usage == nil || got.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", got.Usage)
	}
	if got.Error == nil || got.Error.Code != "provider_timeout" || !got.Error.Retryable {
		t.Fatalf("error = %#v", got.Error)
	}
	if got.Error.Classification != providers.ErrorClassTransport {
		t.Fatalf("error classification = %q, want %q", got.Error.Classification, providers.ErrorClassTransport)
	}
	if got.Cancellation == nil || got.Cancellation.Reason != "caller_cancelled" {
		t.Fatalf("cancellation = %#v", got.Cancellation)
	}
	if got.Cancellation.Classification != providers.ErrorClassCancellation {
		t.Fatalf("cancellation classification = %q, want %q", got.Cancellation.Classification, providers.ErrorClassCancellation)
	}
	if got.Cancellation.OutputState != providers.ErrorClassPartialOutput {
		t.Fatalf("cancellation output state = %q, want %q", got.Cancellation.OutputState, providers.ErrorClassPartialOutput)
	}
}
