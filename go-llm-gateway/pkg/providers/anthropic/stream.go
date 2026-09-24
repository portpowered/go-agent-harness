package anthropic

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const defaultIndex = 0

// Anthropic content block types tracked while translating a stream.
const (
	anthropicBlockText     = "text"
	anthropicBlockThinking = "thinking"
	anthropicBlockToolUse  = "tool_use"
)

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
	state := &anthropicStreamState{ch: ch}

	for stream.Next() {
		if state.handleEvent(stream.Current()) {
			return
		}
	}

	state.endCurrentBlock()

	// Check for stream errors before sending MESSAGE.END so consumers
	// can properly handle partial responses.
	if err := stream.Err(); err != nil {
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeError,
			ActorProvidedIndex: 0,
			Value:              providers.NewStreamTransportErrorValue(err),
		}
	}

	state.sendMessageEnd()
}

// anthropicStreamState tracks the open content block and usage while one
// Anthropic stream is translated into gateway StreamMessages.
type anthropicStreamState struct {
	ch             chan<- messages.StreamMessage
	curBlockType   string // "text", "thinking", "tool_use"
	toolCallArgs   string
	toolCallID     string
	toolCallName   string
	toolCallIndex  int
	lastUsage      messages.TokenUsage
	messageEndSent bool
}

// handleEvent translates one stream event and reports whether the stream
// reached message_stop and translation is complete.
func (s *anthropicStreamState) handleEvent(event anthropic.MessageStreamEventUnion) bool {
	switch event.Type {
	case "message_start":
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeMessageStart,
			ActorProvidedIndex: defaultIndex,
			Value:              messages.NewMessageStartValue(),
		}
	case "content_block_start":
		s.endCurrentBlock()
		s.startBlock(event.ContentBlock, int(event.Index))
	case "content_block_delta":
		s.applyDelta(event.Delta, int(event.Index))
	case "content_block_stop":
		s.endCurrentBlock()
	case "message_delta":
		s.lastUsage = messages.TokenUsage{
			PromptTokens:     int(event.Usage.InputTokens),
			CompletionTokens: int(event.Usage.OutputTokens),
			TotalTokens:      int(event.Usage.InputTokens + event.Usage.OutputTokens),
		}
	case "message_stop":
		s.endCurrentBlock()
		s.sendMessageEnd()
		return true
	}
	return false
}

// startBlock opens the content block announced by content_block_start.
func (s *anthropicStreamState) startBlock(blk anthropic.ContentBlockStartEventContentBlockUnion, idx int) {
	switch blk.Type {
	case anthropicBlockText:
		s.openTextBlock()
	case anthropicBlockThinking:
		s.openThinkingBlock()
	case anthropicBlockToolUse:
		s.curBlockType = anthropicBlockToolUse
		s.toolCallIndex = idx
		s.toolCallID = blk.ID
		s.toolCallName = blk.Name
		s.toolCallArgs = ""
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeToolCallStart,
			ActorProvidedIndex: idx,
			Value:              messages.NewToolCallStartValue(blk.ID, blk.Name),
		}
	}
}

// applyDelta forwards a content_block_delta, implicitly opening a text or
// thinking block when the provider streams a delta without a matching start.
func (s *anthropicStreamState) applyDelta(delta anthropic.MessageStreamEventUnionDelta, idx int) {
	switch delta.Type {
	case "text_delta":
		if s.curBlockType != anthropicBlockText {
			s.endCurrentBlock()
			s.openTextBlock()
		}
		if delta.Text != "" {
			s.ch <- messages.StreamMessage{
				Type:               messages.StreamTypeTextDelta,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewTextDeltaValue(delta.Text),
			}
		}
	case "thinking_delta":
		if s.curBlockType != anthropicBlockThinking {
			s.endCurrentBlock()
			s.openThinkingBlock()
		}
		if delta.Thinking != "" {
			s.ch <- messages.StreamMessage{
				Type:               messages.StreamTypeReasoningDelta,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewReasoningDeltaValue(delta.Thinking),
			}
		}
	case "input_json_delta":
		if delta.PartialJSON != "" {
			s.toolCallArgs += delta.PartialJSON
			s.ch <- messages.StreamMessage{
				Type:               messages.StreamTypeToolCallDelta,
				ActorProvidedIndex: idx,
				Value:              messages.NewToolCallDeltaValue(delta.PartialJSON),
			}
		}
	}
}

func (s *anthropicStreamState) openTextBlock() {
	s.curBlockType = anthropicBlockText
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeTextStart,
		ActorProvidedIndex: defaultIndex,
		Value:              messages.NewTextStartValue(),
	}
}

func (s *anthropicStreamState) openThinkingBlock() {
	s.curBlockType = anthropicBlockThinking
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeReasoningStart,
		ActorProvidedIndex: defaultIndex,
		Value:              messages.NewReasoningStartValue(),
	}
}

func (s *anthropicStreamState) sendMessageEnd() {
	if s.messageEndSent {
		return
	}
	s.messageEndSent = true
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageEnd,
		ActorProvidedIndex: defaultIndex,
		Value:              messages.NewMessageEndValue(s.lastUsage),
	}
	// Emit system information for token usage corresponding to this response.
	usage := s.lastUsage
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 || usage.ReasoningTokens != 0 {
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeUsageInfo,
			ActorProvidedIndex: defaultIndex,
			Value:              messages.NewUsageInfoValue(usage),
		}
	}
}

func (s *anthropicStreamState) endCurrentBlock() {
	switch s.curBlockType {
	case anthropicBlockThinking:
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeReasoningEnd,
			ActorProvidedIndex: defaultIndex,
			Value:              messages.NewReasoningEndValue(),
		}
	case anthropicBlockText:
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeTextEnd,
			ActorProvidedIndex: defaultIndex,
			Value:              messages.NewTextEndValue(),
		}
	case anthropicBlockToolUse:
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeToolCallEnd,
			ActorProvidedIndex: s.toolCallIndex,
			Value:              messages.NewToolCallEndValue(s.toolCallID, s.toolCallName, s.toolCallArgs),
		}
	}
	s.curBlockType = ""
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
