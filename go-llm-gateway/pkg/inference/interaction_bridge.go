package inference

import (
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
)

// LoopInteractionEventFromGateway projects a gateway interaction event into the
// loop-owned interaction event shape used by go-agent-loop subsystems.
func LoopInteractionEventFromGateway(event gateway.InteractionEvent) messages.InteractionEvent {
	out := messages.InteractionEvent{
		InteractionID: event.InteractionID,
		Sequence:      event.Sequence,
		Type:          messages.InteractionEventType(event.Type),
		Provider:      event.Provider,
		Model:         event.Model,
	}

	if event.TextDelta != nil {
		out.TextDelta = event.TextDelta.Content
	}
	if event.FinalMessage != nil {
		msg := gatewayInteractionMessageToLoop(*event.FinalMessage)
		out.FinalMessage = &msg
	}
	if event.ToolCall != nil {
		call := messages.ToolCall{
			ID:        event.ToolCall.ID,
			Name:      event.ToolCall.Name,
			Arguments: string(event.ToolCall.Arguments),
		}
		out.ToolCall = &call
	}
	if event.ToolResult != nil {
		call := messages.ToolCall{ID: event.ToolResult.ToolCallID, Name: event.ToolResult.Name}
		out.ToolCall = &call
	}
	if event.Usage != nil {
		usage := messages.TokenUsage{
			PromptTokens:     int(event.Usage.InputTokens),
			CompletionTokens: int(event.Usage.OutputTokens),
			TotalTokens:      int(event.Usage.TotalTokens),
		}
		out.Usage = &usage
	}
	if event.Error != nil {
		out.Error = &messages.InteractionError{
			Code:           event.Error.Code,
			Message:        event.Error.Message,
			Classification: event.Error.Classification,
			Retryable:      event.Error.Retryable,
		}
	}
	if event.Cancellation != nil {
		out.Cancellation = &messages.InteractionCancellation{
			Reason:         event.Cancellation.Reason,
			Message:        event.Cancellation.Message,
			Classification: event.Cancellation.Classification,
			OutputState:    event.Cancellation.OutputState,
		}
	}

	return out
}

func gatewayInteractionMessageToLoop(msg gateway.InteractionMessage) messages.Message {
	out := messages.Message{
		Role:       messages.Role(msg.Role),
		ToolCalls:  make([]messages.ToolCall, 0, len(msg.ToolCalls)),
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	}

	for _, part := range msg.ContentParts {
		switch part.Type {
		case gateway.InteractionContentText:
			out.ContentParts = append(out.ContentParts, messages.TextPart{Text: part.Text})
		case gateway.InteractionContentImage:
			out.ContentParts = append(out.ContentParts, messages.ImagePart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case gateway.InteractionContentAudio:
			out.ContentParts = append(out.ContentParts, messages.AudioPart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case gateway.InteractionContentVideo:
			out.ContentParts = append(out.ContentParts, messages.VideoPart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case gateway.InteractionContentFile:
			out.ContentParts = append(out.ContentParts, messages.FilePart{URL: part.URL, Bytes: part.Bytes, Name: part.Name, MediaType: part.MediaType})
		}
	}
	for _, call := range msg.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, messages.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: string(call.Arguments),
		})
	}

	return out
}
