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
	name     string
	captured providers.InferenceRequest
	response providers.InferenceResponse
	err      error
}

func (p *fakeInteractionProvider) Name() string {
	return p.name
}

func (p *fakeInteractionProvider) Infer(_ context.Context, req providers.InferenceRequest) (providers.InferenceResponse, error) {
	p.captured = req
	if p.err != nil {
		return providers.InferenceResponse{}, p.err
	}
	return p.response, nil
}

func (p *fakeInteractionProvider) InferStream(_ context.Context, req providers.InferenceRequest) (<-chan messages.StreamMessage, error) {
	p.captured = req
	ch := make(chan messages.StreamMessage)
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

func collectInteractionEvents(t *testing.T, gw *DefaultGateway, req InteractionRequest) []InteractionEvent {
	t.Helper()

	ch, err := gw.Interact(context.Background(), req)
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
