package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// Interact runs a provider-backed model interaction and emits the normalized
// PNIG event contract.
func (g *DefaultGateway) Interact(ctx context.Context, req InteractionRequest) (<-chan InteractionEvent, error) {
	interactionID := req.InteractionID
	if interactionID == "" {
		interactionID = newInteractionID()
	}

	out := make(chan InteractionEvent)
	go func() {
		defer close(out)

		emitter := newInteractionEventEmitter(out, interactionID, g.provider.Name(), req.Model)
		if !emitter.emit(ctx, InteractionEvent{Type: InteractionEventStart}) {
			return
		}

		resp, err := g.provider.Infer(ctx, interactionProviderRequest(req))
		if err != nil {
			_ = emitter.emit(ctx, InteractionEvent{
				Type:  InteractionEventError,
				Error: &InteractionError{Code: "provider_error", Message: err.Error()},
			})
			_ = emitter.emit(ctx, InteractionEvent{Type: InteractionEventEnd})
			return
		}

		text := resp.Message.TextContent()
		if text != "" {
			if !emitter.emit(ctx, InteractionEvent{
				Type:      InteractionEventTextDelta,
				TextDelta: &TextDeltaEvent{Content: text},
			}) {
				return
			}
		}

		if !emitter.emit(ctx, InteractionEvent{
			Type:         InteractionEventFinalMessage,
			FinalMessage: interactionMessageFromModel(resp.Message),
		}) {
			return
		}

		if usage, ok := interactionUsageFromModel(resp.Usage); ok {
			if !emitter.emit(ctx, InteractionEvent{
				Type:  InteractionEventUsage,
				Usage: &usage,
			}) {
				return
			}
		}

		_ = emitter.emit(ctx, InteractionEvent{Type: InteractionEventEnd})
	}()

	return out, nil
}

type interactionEventEmitter struct {
	out           chan<- InteractionEvent
	interactionID string
	provider      string
	model         string
	sequence      int64
}

func newInteractionEventEmitter(out chan<- InteractionEvent, interactionID, provider, model string) *interactionEventEmitter {
	return &interactionEventEmitter{
		out:           out,
		interactionID: interactionID,
		provider:      provider,
		model:         model,
	}
}

func (e *interactionEventEmitter) emit(ctx context.Context, event InteractionEvent) bool {
	e.sequence++
	now := time.Now().UTC()
	event.InteractionID = e.interactionID
	event.Sequence = e.sequence
	event.Provider = e.provider
	event.Model = e.model
	event.CreatedAt = &now

	select {
	case <-ctx.Done():
		return false
	case e.out <- event:
		return true
	}
}

func interactionProviderRequest(req InteractionRequest) providers.InferenceRequest {
	return providers.InferenceRequest{
		Messages: interactionMessagesToModel(req),
		Tools:    interactionToolsToModel(req.Tools),
		Model:    req.Model,
		Config:   req.Config,
	}
}

func interactionMessagesToModel(req InteractionRequest) []models.Message {
	msgs := make([]models.Message, 0, len(req.SystemInstructions)+len(req.Messages))
	for _, instruction := range req.SystemInstructions {
		msgs = append(msgs, models.NewTextMessage(models.RoleSystem, instruction))
	}
	for _, msg := range req.Messages {
		msgs = append(msgs, interactionMessageToModel(msg))
	}
	return msgs
}

func interactionMessageToModel(msg InteractionMessage) models.Message {
	return models.Message{
		Role:         modelRoleFromInteraction(msg.Role),
		ContentParts: interactionContentToModel(msg.ContentParts),
		ToolCalls:    interactionToolCallsToModel(msg.ToolCalls),
		ToolCallID:   msg.ToolCallID,
		Name:         msg.Name,
	}
}

func interactionMessageFromModel(msg models.Message) *InteractionMessage {
	return &InteractionMessage{
		Role:         interactionRoleFromModel(msg.Role),
		ContentParts: interactionContentFromModel(msg.ContentParts),
		ToolCalls:    interactionToolCallsFromModel(msg.ToolCalls),
		ToolCallID:   msg.ToolCallID,
		Name:         msg.Name,
	}
}

func interactionContentToModel(parts []InteractionContent) []models.ContentPart {
	out := make([]models.ContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case InteractionContentText:
			out = append(out, models.TextPart{Text: part.Text})
		case InteractionContentImage:
			out = append(out, models.ImagePart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case InteractionContentAudio:
			out = append(out, models.AudioPart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case InteractionContentVideo:
			out = append(out, models.VideoPart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case InteractionContentFile:
			out = append(out, messages.FilePart{URL: part.URL, Bytes: part.Bytes, Name: part.Name, MediaType: part.MediaType})
		}
	}
	return out
}

func interactionContentFromModel(parts []models.ContentPart) []InteractionContent {
	out := make([]InteractionContent, 0, len(parts))
	for _, part := range parts {
		switch p := part.(type) {
		case models.TextPart:
			out = append(out, InteractionContent{Type: InteractionContentText, Text: p.Text})
		case models.ImagePart:
			out = append(out, InteractionContent{Type: InteractionContentImage, URL: p.URL, Bytes: p.Bytes, MediaType: p.MediaType})
		case models.AudioPart:
			out = append(out, InteractionContent{Type: InteractionContentAudio, URL: p.URL, Bytes: p.Bytes, MediaType: p.MediaType})
		case models.VideoPart:
			out = append(out, InteractionContent{Type: InteractionContentVideo, URL: p.URL, Bytes: p.Bytes, MediaType: p.MediaType})
		case messages.FilePart:
			out = append(out, InteractionContent{Type: InteractionContentFile, URL: p.URL, Bytes: p.Bytes, Name: p.Name, MediaType: p.MediaType})
		}
	}
	return out
}

func interactionToolsToModel(tools []InteractionTool) []models.ToolDefinition {
	out := make([]models.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		params := make([]models.ToolParameter, 0, len(tool.Parameters))
		for _, param := range tool.Parameters {
			params = append(params, models.ToolParameter{
				Name:        param.Name,
				Type:        param.Type,
				Description: param.Description,
				Required:    param.Required,
			})
		}
		out = append(out, models.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  params,
		})
	}
	return out
}

func interactionToolCallsToModel(calls []InteractionToolCall) []models.ToolCall {
	out := make([]models.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, models.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: string(call.Arguments),
		})
	}
	return out
}

func interactionToolCallsFromModel(calls []models.ToolCall) []InteractionToolCall {
	out := make([]InteractionToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, InteractionToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: json.RawMessage(call.Arguments),
		})
	}
	return out
}

func interactionUsageFromModel(usage models.TokenUsage) (InteractionUsage, bool) {
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 && usage.ReasoningTokens == 0 {
		return InteractionUsage{}, false
	}
	return InteractionUsage{
		InputTokens:  int64(usage.PromptTokens),
		OutputTokens: int64(usage.CompletionTokens),
		TotalTokens:  int64(usage.TotalTokens),
	}, true
}

func modelRoleFromInteraction(role InteractionRole) models.Role {
	switch role {
	case InteractionRoleAssistant:
		return models.RoleAssistant
	case InteractionRoleTool:
		return models.RoleTool
	case InteractionRoleSystem:
		return models.RoleSystem
	default:
		return models.RoleUser
	}
}

func interactionRoleFromModel(role models.Role) InteractionRole {
	switch role {
	case models.RoleAssistant:
		return InteractionRoleAssistant
	case models.RoleTool:
		return InteractionRoleTool
	case models.RoleSystem:
		return InteractionRoleSystem
	default:
		return InteractionRoleUser
	}
}

func newInteractionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "interaction-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("interaction-%d", time.Now().UTC().UnixNano())
}
