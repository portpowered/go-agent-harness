package gateway

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestInteractionRequestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	req := InteractionRequest{
		InteractionID:        "interaction-123",
		ContinueFromSequence: 3,
		Provider:             "test-provider",
		Model:                "test-model",
		SystemInstructions:   []string{"Be concise.", "Use tools when needed."},
		Messages: []InteractionMessage{
			{
				Role: InteractionRoleUser,
				ContentParts: []InteractionContent{
					{Type: InteractionContentText, Text: "What is the weather?"},
					{Type: InteractionContentImage, URL: "https://example.test/image.png", MediaType: "image/png"},
				},
			},
			{
				Role:       InteractionRoleTool,
				ToolCallID: "call-weather",
				Name:       "weather",
				ContentParts: []InteractionContent{
					{Type: InteractionContentText, Text: "72F"},
				},
			},
		},
		Tools: []InteractionTool{
			{
				Name:        "weather",
				Description: "Get current weather.",
				Parameters: []InteractionToolParameter{
					{Name: "city", Type: "string", Description: "City name", Required: true},
				},
				Metadata: map[string]json.RawMessage{
					"owner": json.RawMessage(`"forecast-system"`),
				},
			},
		},
		ToolResults: []InteractionToolResult{
			{ToolCallID: "call-weather", Name: "weather", Payload: json.RawMessage(`{"temperature":72}`)},
		},
		Metadata: map[string]json.RawMessage{
			"traceId": json.RawMessage(`"trace-123"`),
		},
		Config: json.RawMessage(`{"temperature":0.2}`),
	}

	var got InteractionRequest
	roundTripJSON(t, req, &got)

	if !reflect.DeepEqual(got, req) {
		t.Fatalf("request mismatch after JSON round trip:\n got: %#v\nwant: %#v", got, req)
	}
}

func TestInteractionEventsJSONRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	events := []InteractionEvent{
		{
			InteractionID: "interaction-123",
			Sequence:      1,
			Type:          InteractionEventStart,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
		},
		{
			InteractionID: "interaction-123",
			Sequence:      2,
			Type:          InteractionEventTextDelta,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Correlation:   InteractionCorrelation{MessageID: "message-1"},
			TextDelta:     &TextDeltaEvent{Content: "hello"},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      3,
			Type:          InteractionEventToolCallRequest,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Correlation:   InteractionCorrelation{MessageID: "message-1", ToolCallID: "call-weather"},
			ToolCall:      &InteractionToolCall{ID: "call-weather", Name: "weather", Arguments: json.RawMessage(`{"city":"Boston"}`)},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      4,
			Type:          InteractionEventToolResultAccepted,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Correlation:   InteractionCorrelation{ToolCallID: "call-weather"},
			ToolResult:    &InteractionToolResult{ToolCallID: "call-weather", Name: "weather", Payload: json.RawMessage(`{"temperature":72}`)},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      5,
			Type:          InteractionEventFinalMessage,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Correlation:   InteractionCorrelation{MessageID: "message-2"},
			FinalMessage: &InteractionMessage{
				Role:         InteractionRoleAssistant,
				ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "It is 72F."}},
			},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      6,
			Type:          InteractionEventUsage,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Usage:         &InteractionUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      7,
			Type:          InteractionEventError,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Error:         &InteractionError{Code: "provider_error", Message: "provider failed", Retryable: true},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      8,
			Type:          InteractionEventCancellation,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Cancellation:  &InteractionCancellation{Reason: "caller_cancelled", Message: "context canceled"},
		},
		{
			InteractionID: "interaction-123",
			Sequence:      9,
			Type:          InteractionEventEnd,
			Provider:      "test-provider",
			Model:         "test-model",
			CreatedAt:     &now,
			Metadata: map[string]json.RawMessage{
				"finished": json.RawMessage(`true`),
			},
		},
	}

	var got []InteractionEvent
	roundTripJSON(t, events, &got)

	if !reflect.DeepEqual(got, events) {
		t.Fatalf("events mismatch after JSON round trip:\n got: %#v\nwant: %#v", got, events)
	}
}

func TestInteractionEventProvenanceAndSequenceContract(t *testing.T) {
	t.Parallel()

	events := []InteractionEvent{
		{InteractionID: "interaction-123", Sequence: 1, Type: InteractionEventStart, Provider: "test-provider", Model: "test-model"},
		{InteractionID: "interaction-123", Sequence: 2, Type: InteractionEventTextDelta, Provider: "test-provider", Model: "test-model"},
		{InteractionID: "interaction-123", Sequence: 3, Type: InteractionEventEnd, Provider: "test-provider", Model: "test-model"},
	}

	for i, event := range events {
		if event.InteractionID == "" {
			t.Fatalf("event %d missing interaction ID", i)
		}
		if event.Provider == "" {
			t.Fatalf("event %d missing provider", i)
		}
		if event.Model == "" {
			t.Fatalf("event %d missing model", i)
		}
		if event.Sequence != int64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, i+1)
		}
	}
}

func roundTripJSON[T any](t *testing.T, in T, out *T) {
	t.Helper()

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
