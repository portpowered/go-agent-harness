package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

type fakeInteractionProvider struct {
	name           string
	captured       providers.InferenceRequest
	calls          int
	response       providers.InferenceResponse
	err            error
	streamMessages []messages.StreamMessage
	streamErr      error
}

func (p *fakeInteractionProvider) Name() string {
	return p.name
}

func (p *fakeInteractionProvider) Infer(_ context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.captured = req
	p.calls++
	if p.err != nil {
		return providers.InferenceResponse{}, p.err
	}
	return p.response, nil
}

func (p *fakeInteractionProvider) InferStream(_ context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.captured = req
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	ch := make(chan messages.StreamMessage, len(p.streamMessages))
	for _, msg := range p.streamMessages {
		ch <- msg
	}
	close(ch)
	return ch, nil
}

func TestInteract_NormalizesProviderTextResponse(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		response: providers.InferenceResponse{
			Message: models.NewTextMessage(models.RoleAssistant, "hello from provider"),
			Usage:   models.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID:      "interaction-123",
		Provider:           "ignored-request-provider",
		Model:              "model-a",
		SystemInstructions: []string{"Be concise."},
		Messages: []InteractionMessage{
			{
				Role:         InteractionRoleUser,
				ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "Say hello"}},
			},
		},
		Tools: []InteractionTool{
			{
				Name:        "lookup",
				Description: "Look up facts.",
				Parameters: []InteractionToolParameter{
					{Name: "query", Type: "string", Description: "Search query", Required: true},
				},
			},
		},
		Config: json.RawMessage(`{"temperature":0.1}`),
	})

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventTextDelta,
		InteractionEventFinalMessage,
		InteractionEventUsage,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}

	for i, event := range events {
		if event.InteractionID != "interaction-123" {
			t.Fatalf("events[%d].InteractionID = %q", i, event.InteractionID)
		}
		if event.Sequence != int64(i+1) {
			t.Fatalf("events[%d].Sequence = %d, want %d", i, event.Sequence, i+1)
		}
		if event.Provider != "fake-provider" {
			t.Fatalf("events[%d].Provider = %q", i, event.Provider)
		}
		if event.Model != "model-a" {
			t.Fatalf("events[%d].Model = %q", i, event.Model)
		}
		if event.CreatedAt == nil {
			t.Fatalf("events[%d].CreatedAt is nil", i)
		}
	}
	if events[1].TextDelta == nil || events[1].TextDelta.Content != "hello from provider" {
		t.Fatalf("text delta = %#v", events[1].TextDelta)
	}
	if events[2].FinalMessage == nil || events[2].FinalMessage.Role != InteractionRoleAssistant {
		t.Fatalf("final message = %#v", events[2].FinalMessage)
	}
	if got := events[2].FinalMessage.ContentParts; len(got) != 1 || got[0].Text != "hello from provider" {
		t.Fatalf("final content = %#v", got)
	}
	if events[3].Usage == nil || events[3].Usage.InputTokens != 7 || events[3].Usage.OutputTokens != 3 || events[3].Usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v", events[3].Usage)
	}

	if len(provider.captured.Messages) != 2 {
		t.Fatalf("provider messages = %#v, want system + user", provider.captured.Messages)
	}
	if provider.captured.Messages[0].Role != models.RoleSystem || provider.captured.Messages[0].TextContent() != "Be concise." {
		t.Fatalf("system instruction translation = %#v", provider.captured.Messages[0])
	}
	if provider.captured.Messages[1].Role != models.RoleUser || provider.captured.Messages[1].TextContent() != "Say hello" {
		t.Fatalf("user message translation = %#v", provider.captured.Messages[1])
	}
	if len(provider.captured.Tools) != 1 || provider.captured.Tools[0].Name != "lookup" || len(provider.captured.Tools[0].Parameters) != 1 {
		t.Fatalf("tool translation = %#v", provider.captured.Tools)
	}
	if string(provider.captured.Config) != `{"temperature":0.1}` {
		t.Fatalf("config = %s", provider.captured.Config)
	}
}

func TestInteract_EmptyProviderOutputCompletesWithEmptyFinalMessage(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		response: providers.InferenceResponse{
			Message: models.Message{Role: models.RoleAssistant},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID: "interaction-empty",
		Model:         "model-empty",
	})

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventFinalMessage,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[1].FinalMessage == nil {
		t.Fatal("final message is nil")
	}
	if events[1].FinalMessage.Role != InteractionRoleAssistant {
		t.Fatalf("final role = %q", events[1].FinalMessage.Role)
	}
	if len(events[1].FinalMessage.ContentParts) != 0 {
		t.Fatalf("final content = %#v, want empty", events[1].FinalMessage.ContentParts)
	}
}

func TestInteract_NormalizesProviderError(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		err:  errors.New("upstream failed"),
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID: "interaction-error",
		Model:         "model-error",
	})

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventError,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[1].Error == nil {
		t.Fatal("error event payload is nil")
	}
	if events[1].Error.Code != "provider_error" {
		t.Fatalf("error code = %q", events[1].Error.Code)
	}
	if events[1].Error.Message != "upstream failed" {
		t.Fatalf("error message = %q", events[1].Error.Message)
	}
}

func TestInteract_EmitsCancellationWhenContextCancelledBeforeProviderReturns(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		err:  context.Canceled,
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := collectInteractionEventsFromContext(t, ctx, gw, InteractionRequest{
		InteractionID: "interaction-cancelled",
		Model:         "model-cancelled",
	})

	wantTypes := []InteractionEventType{
		InteractionEventCancellation,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[0].Cancellation == nil {
		t.Fatal("cancellation payload is nil")
	}
	if events[0].Cancellation.Reason != "caller_cancelled" {
		t.Fatalf("cancellation reason = %q", events[0].Cancellation.Reason)
	}
	if events[0].Cancellation.Message != context.Canceled.Error() {
		t.Fatalf("cancellation message = %q", events[0].Cancellation.Message)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func TestInteract_PreservesPartialOutputBeforeCancellation(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		response: providers.InferenceResponse{
			Message: models.NewTextMessage(models.RoleAssistant, "partial output"),
			Usage:   models.TokenUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := gw.Interact(ctx, InteractionRequest{
		InteractionID: "interaction-partial-cancel",
		Model:         "model-cancelled",
	})
	if err != nil {
		t.Fatalf("Interact: %v", err)
	}

	var events []InteractionEvent
	for event := range ch {
		events = append(events, event)
		if event.Type == InteractionEventTextDelta {
			cancel()
		}
	}

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventTextDelta,
		InteractionEventCancellation,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[1].TextDelta == nil || events[1].TextDelta.Content != "partial output" {
		t.Fatalf("text delta = %#v", events[1].TextDelta)
	}
	if events[2].Cancellation == nil || events[2].Cancellation.Reason != "caller_cancelled" {
		t.Fatalf("cancellation = %#v", events[2].Cancellation)
	}
	if events[3].Type != InteractionEventEnd {
		t.Fatalf("terminal event = %#v", events[3])
	}
}

func TestInteract_NormalizesDeadlineExceededAsTimeoutError(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		err:  context.DeadlineExceeded,
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID: "interaction-timeout",
		Model:         "model-timeout",
	})

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventError,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[1].Error == nil {
		t.Fatal("error payload is nil")
	}
	if events[1].Error.Code != "provider_timeout" {
		t.Fatalf("error code = %q", events[1].Error.Code)
	}
	if !events[1].Error.Retryable {
		t.Fatalf("retryable = %v, want true", events[1].Error.Retryable)
	}
	if events[1].Error.Message != context.DeadlineExceeded.Error() {
		t.Fatalf("error message = %q", events[1].Error.Message)
	}
}

func TestInteract_EmitsToolCallRequestsAndPausesForHandoff(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		response: providers.InferenceResponse{
			Message: models.Message{
				Role:         models.RoleAssistant,
				ContentParts: []models.ContentPart{models.TextPart{Text: "I need current data."}},
				ToolCalls: []models.ToolCall{
					{ID: "call-a", Name: "lookup", Arguments: `{"query":"a"}`},
					{ID: "", Name: "forecast", Arguments: `{"city":"Boston"}`},
				},
			},
			Usage: models.TokenUsage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 15},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID: "interaction-tools",
		Model:         "model-tools",
		Messages: []InteractionMessage{
			{
				Role:         InteractionRoleUser,
				ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "Check this"}},
			},
		},
	})

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventTextDelta,
		InteractionEventToolCallRequest,
		InteractionEventToolCallRequest,
		InteractionEventUsage,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if events[2].ToolCall == nil || events[2].ToolCall.ID != "call-a" || events[2].ToolCall.Name != "lookup" {
		t.Fatalf("first tool call = %#v", events[2].ToolCall)
	}
	if string(events[2].ToolCall.Arguments) != `{"query":"a"}` {
		t.Fatalf("first tool args = %s", events[2].ToolCall.Arguments)
	}
	if events[2].Correlation.ToolCallID != "call-a" {
		t.Fatalf("first correlation = %#v", events[2].Correlation)
	}
	if events[3].ToolCall == nil || events[3].ToolCall.ID != "tool-call-2" || events[3].ToolCall.Name != "forecast" {
		t.Fatalf("second tool call = %#v", events[3].ToolCall)
	}
	if events[3].Correlation.ToolCallID != "tool-call-2" {
		t.Fatalf("second correlation = %#v", events[3].Correlation)
	}
}

func TestInteract_AcceptsToolResultsAndContinuesSequence(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		response: providers.InferenceResponse{
			Message: models.NewTextMessage(models.RoleAssistant, "The answer is ready."),
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	events := collectInteractionEvents(t, gw, InteractionRequest{
		InteractionID:        "interaction-tools",
		ContinueFromSequence: 6,
		Model:                "model-tools",
		Messages: []InteractionMessage{
			{
				Role:         InteractionRoleUser,
				ContentParts: []InteractionContent{{Type: InteractionContentText, Text: "Check this"}},
			},
			{
				Role: InteractionRoleAssistant,
				ToolCalls: []InteractionToolCall{
					{ID: "call-a", Name: "lookup", Arguments: json.RawMessage(`{"query":"a"}`)},
					{ID: "call-b", Name: "forecast", Arguments: json.RawMessage(`{"city":"Boston"}`)},
				},
			},
		},
		ToolResults: []InteractionToolResult{
			{ToolCallID: "call-a", Name: "lookup", Payload: json.RawMessage(`{"value":"alpha"}`)},
			{ToolCallID: "call-b", Name: "forecast", Content: "sunny"},
		},
	})

	wantTypes := []InteractionEventType{
		InteractionEventStart,
		InteractionEventToolResultAccepted,
		InteractionEventToolResultAccepted,
		InteractionEventTextDelta,
		InteractionEventFinalMessage,
		InteractionEventEnd,
	}
	if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	for i, event := range events {
		wantSequence := int64(i + 7)
		if event.Sequence != wantSequence {
			t.Fatalf("events[%d].Sequence = %d, want %d", i, event.Sequence, wantSequence)
		}
	}
	if events[1].ToolResult == nil || events[1].ToolResult.ToolCallID != "call-a" {
		t.Fatalf("first accepted result = %#v", events[1].ToolResult)
	}
	if events[1].Correlation.ToolCallID != "call-a" || events[2].Correlation.ToolCallID != "call-b" {
		t.Fatalf("accepted correlations = %#v %#v", events[1].Correlation, events[2].Correlation)
	}

	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if len(provider.captured.Messages) != 4 {
		t.Fatalf("provider messages = %#v, want user + assistant + 2 tool results", provider.captured.Messages)
	}
	if provider.captured.Messages[2].Role != models.RoleTool || provider.captured.Messages[2].ToolCallID != "call-a" {
		t.Fatalf("first provider tool result = %#v", provider.captured.Messages[2])
	}
	if provider.captured.Messages[2].TextContent() != `{"value":"alpha"}` {
		t.Fatalf("first provider tool content = %q", provider.captured.Messages[2].TextContent())
	}
	if provider.captured.Messages[3].Role != models.RoleTool || provider.captured.Messages[3].ToolCallID != "call-b" {
		t.Fatalf("second provider tool result = %#v", provider.captured.Messages[3])
	}
	if provider.captured.Messages[3].TextContent() != "sunny" {
		t.Fatalf("second provider tool content = %q", provider.captured.Messages[3].TextContent())
	}
}

func TestInteract_RejectsInvalidToolResultsBeforeProviderContinuation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		results     []InteractionToolResult
		wantMessage string
	}{
		{
			name: "missing",
			results: []InteractionToolResult{
				{ToolCallID: "call-a", Name: "lookup", Content: "alpha"},
			},
			wantMessage: `missing tool result for tool call "call-b"`,
		},
		{
			name: "duplicate",
			results: []InteractionToolResult{
				{ToolCallID: "call-a", Name: "lookup", Content: "alpha"},
				{ToolCallID: "call-a", Name: "lookup", Content: "alpha again"},
			},
			wantMessage: `duplicate tool result for tool call "call-a"`,
		},
		{
			name: "unknown",
			results: []InteractionToolResult{
				{ToolCallID: "call-a", Name: "lookup", Content: "alpha"},
				{ToolCallID: "call-c", Name: "unknown", Content: "gamma"},
			},
			wantMessage: `unknown tool result "call-c"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &fakeInteractionProvider{
				name:     "fake-provider",
				response: providers.InferenceResponse{Message: models.NewTextMessage(models.RoleAssistant, "should not call")},
			}
			gw, err := NewGateway(WithProvider(provider))
			if err != nil {
				t.Fatalf("NewGateway: %v", err)
			}

			events := collectInteractionEvents(t, gw, InteractionRequest{
				InteractionID: "interaction-invalid-tools",
				Model:         "model-tools",
				Messages: []InteractionMessage{
					{
						Role: InteractionRoleAssistant,
						ToolCalls: []InteractionToolCall{
							{ID: "call-a", Name: "lookup", Arguments: json.RawMessage(`{"query":"a"}`)},
							{ID: "call-b", Name: "forecast", Arguments: json.RawMessage(`{"city":"Boston"}`)},
						},
					},
				},
				ToolResults: tt.results,
			})

			wantTypes := []InteractionEventType{
				InteractionEventStart,
				InteractionEventError,
				InteractionEventEnd,
			}
			if got := interactionEventTypes(events); !reflect.DeepEqual(got, wantTypes) {
				t.Fatalf("event types = %v, want %v", got, wantTypes)
			}
			if events[1].Error == nil {
				t.Fatal("validation error payload is nil")
			}
			if events[1].Error.Code != "tool_result_validation_error" {
				t.Fatalf("error code = %q", events[1].Error.Code)
			}
			if events[1].Error.Message != tt.wantMessage {
				t.Fatalf("error message = %q, want %q", events[1].Error.Message, tt.wantMessage)
			}
			if provider.calls != 0 {
				t.Fatalf("provider calls = %d, want 0", provider.calls)
			}
		})
	}
}

func collectInteractionEvents(t *testing.T, gw *DefaultGateway, req InteractionRequest) []InteractionEvent {
	t.Helper()

	return collectInteractionEventsFromContext(t, context.Background(), gw, req)
}

func collectInteractionEventsFromContext(t *testing.T, ctx context.Context, gw *DefaultGateway, req InteractionRequest) []InteractionEvent {
	t.Helper()

	ch, err := gw.Interact(ctx, req)
	if err != nil {
		t.Fatalf("Interact: %v", err)
	}

	var events []InteractionEvent
	for event := range ch {
		events = append(events, event)
	}
	return events
}

func interactionEventTypes(events []InteractionEvent) []InteractionEventType {
	types := make([]InteractionEventType, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}
