package anthropic

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

const defaultIndex = 0

// messageStream is the subset of the Anthropic streaming API we use (for testing with a mock).
type messageStream interface {
	Next() bool
	Current() anthropic.MessageStreamEventUnion
	Err() error
}

// streamAnthropicToGateway reads Anthropic stream events and sends gateway StreamMessages.
// Emits MESSAGE.START, then content blocks (TEXT.*, REASONING.*, TOOLCALL.*), then MESSAGE.END with usage.
// Caller must not close ch; streamAnthropicToGateway closes it when the stream ends.
func streamAnthropicToGateway(stream messageStream, ch chan<- messages.StreamMessage) {
	defer close(ch)
	var (
		curBlockType   string // "text", "thinking", "tool_use"
		toolCallArgs   string
		toolCallID     string
		toolCallName   string
		toolCallIndex  int
		lastUsage      messages.TokenUsage
		messageEndSent bool
	)

	sendMessageEnd := func() {
		if messageEndSent {
			return
		}
		messageEndSent = true
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeMessageEnd,
			ActorProvidedIndex: defaultIndex,
			Value:              messages.NewMessageEndValue(lastUsage),
		}
		// Emit system information for token usage corresponding to this response.
		if lastUsage.PromptTokens != 0 || lastUsage.CompletionTokens != 0 || lastUsage.TotalTokens != 0 || lastUsage.ReasoningTokens != 0 {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeUsageInfo,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewUsageInfoValue(lastUsage),
			}
		}
	}

	endCurrentBlock := func() {
		switch curBlockType {
		case "thinking":
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeReasoningEnd,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewReasoningEndValue(),
			}
		case "text":
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeTextEnd,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewTextEndValue(),
			}
		case "tool_use":
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeToolCallEnd,
				ActorProvidedIndex: toolCallIndex,
				Value:              messages.NewToolCallEndValue(toolCallID, toolCallName, toolCallArgs),
			}
		}
		curBlockType = ""
	}

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeMessageStart,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewMessageStartValue(),
			}
		case "content_block_start":
			endCurrentBlock()
			blk := event.ContentBlock
			idx := int(event.Index)
			switch blk.Type {
			case "text":
				curBlockType = "text"
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeTextStart,
					ActorProvidedIndex: defaultIndex,
					Value:              messages.NewTextStartValue(),
				}
			case "thinking":
				curBlockType = "thinking"
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeReasoningStart,
					ActorProvidedIndex: defaultIndex,
					Value:              messages.NewReasoningStartValue(),
				}
			case "tool_use":
				curBlockType = "tool_use"
				toolCallIndex = idx
				toolCallID = blk.ID
				toolCallName = blk.Name
				toolCallArgs = ""
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeToolCallStart,
					ActorProvidedIndex: idx,
					Value:              messages.NewToolCallStartValue(blk.ID, blk.Name),
				}
			}
		case "content_block_delta":
			delta := event.Delta
			switch delta.Type {
			case "text_delta":
				if curBlockType != "text" {
					endCurrentBlock()
					curBlockType = "text"
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeTextStart,
						ActorProvidedIndex: defaultIndex,
						Value:              messages.NewTextStartValue(),
					}
				}
				if delta.Text != "" {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeTextDelta,
						ActorProvidedIndex: defaultIndex,
						Value:              messages.NewTextDeltaValue(delta.Text),
					}
				}
			case "thinking_delta":
				if curBlockType != "thinking" {
					endCurrentBlock()
					curBlockType = "thinking"
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeReasoningStart,
						ActorProvidedIndex: defaultIndex,
						Value:              messages.NewReasoningStartValue(),
					}
				}
				if delta.Thinking != "" {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeReasoningDelta,
						ActorProvidedIndex: defaultIndex,
						Value:              messages.NewReasoningDeltaValue(delta.Thinking),
					}
				}
			case "input_json_delta":
				if delta.PartialJSON != "" {
					toolCallArgs += delta.PartialJSON
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeToolCallDelta,
						ActorProvidedIndex: int(event.Index),
						Value:              messages.NewToolCallDeltaValue(delta.PartialJSON),
					}
				}
			}
		case "content_block_stop":
			endCurrentBlock()
		case "message_delta":
			lastUsage = messages.TokenUsage{
				PromptTokens:     int(event.Usage.InputTokens),
				CompletionTokens: int(event.Usage.OutputTokens),
				TotalTokens:      int(event.Usage.InputTokens + event.Usage.OutputTokens),
			}
		case "message_stop":
			endCurrentBlock()
			sendMessageEnd()
			return
		}
	}

	endCurrentBlock()

	// Check for stream errors before sending MESSAGE.END so consumers
	// can properly handle partial responses.
	if err := stream.Err(); err != nil {
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeError,
			ActorProvidedIndex: 0,
			Value:              providers.NewStreamTransportErrorValue(err),
		}
	}

	sendMessageEnd()
}

// AccumulateStreamToMessage consumes a channel of StreamMessages and builds a single gateway Message.
// Used by tests or callers that want to collect stream output into one message.
func AccumulateStreamToMessage(ch <-chan messages.StreamMessage) models.Message {
	var content string
	var toolCalls []models.ToolCall
	for m := range ch {
		switch m.Type {
		case messages.StreamTypeTextDelta:
			if v, ok := m.Value.(*messages.TextDeltaValue); ok {
				content += v.Content
			}
		case messages.StreamTypeToolCallEnd:
			if v, ok := m.Value.(*messages.ToolCallEndValue); ok {
				toolCalls = append(toolCalls, models.ToolCall{ID: v.ToolCallID, Name: v.Name, Arguments: v.Arguments})
			}
		case messages.StreamTypeMessageEnd:
			// Usage is available on MessageEndValue if needed
		}
	}
	return StreamChunksToMessage(content, toolCalls)
}
