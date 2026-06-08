package openai

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/gateway"
)

// contentState represents the current output content type during streaming.
// Transitions (e.g. reasoning → text) trigger an END for the previous type
// before a START for the new one.
type contentState int

const (
	contentStateNone contentState = iota
	contentStateReasoning
	contentStateText
	contentStateAudio
	contentStateToolCall
)

// streamSSEToGateway reads an OpenAI SSE stream from reader and emits gateway StreamMessages on ch.
// It parses SSE data lines, decodes JSON chunks, and maps them to typed events:
// MESSAGE.START, TEXT.*, AUDIO.*, REASONING.*, TOOLCALL.*, USAGE_INFO, MESSAGE.END.
// Supports delta.reasoning for OpenRouter/DeepInfra thinking tokens and delta.audio for audio output.
func streamSSEToGateway(reader io.Reader, ch chan<- messages.StreamMessage) {
	const defaultIndex = 0

	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageStart,
		ActorProvidedIndex: defaultIndex,
		Value:              messages.NewMessageStartValue(),
	}

	var (
		curContentState = contentStateNone
		toolCalls       = make(map[int]struct{ id, name, args string })
		toolCallEnded   = make(map[int]bool)
		lastUsage       messages.TokenUsage
		messageEndSent  bool
		refusalBuf      strings.Builder // accumulates delta.refusal chunks
	)

	sendMessageEnd := func() {
		if messageEndSent {
			return
		}
		// Emit accumulated refusal (if any) before MESSAGE.END.
		if refusalBuf.Len() > 0 {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeRefusal,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewRefusalValue(refusalBuf.String()),
			}
		}
		messageEndSent = true
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeMessageEnd,
			ActorProvidedIndex: defaultIndex,
			Value:              messages.NewMessageEndValue(lastUsage),
		}
		if lastUsage.PromptTokens != 0 || lastUsage.CompletionTokens != 0 || lastUsage.TotalTokens != 0 || lastUsage.ReasoningTokens != 0 {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeUsageInfo,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewUsageInfoValue(lastUsage),
			}
		}
	}

	// endContentState emits the appropriate END event for the current content type.
	endContentState := func() {
		switch curContentState {
		case contentStateReasoning:
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeReasoningEnd,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewReasoningEndValue(),
			}
		case contentStateText:
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeTextEnd,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewTextEndValue(),
			}
		case contentStateAudio:
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeAudioEnd,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewAudioEndValue(),
			}
		case contentStateToolCall:
			for idx, acc := range toolCalls {
				if !toolCallEnded[idx] {
					toolCallEnded[idx] = true
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeToolCallEnd,
						ActorProvidedIndex: idx,
						Value:              messages.NewToolCallEndValue(acc.id, acc.name, acc.args),
					}
				}
			}
		}
		curContentState = contentStateNone
	}

	// transitionTo switches to a new content type: ends current if different, then starts new.
	transitionTo := func(next contentState, startType messages.StreamMessageType, startValue messages.StreamMessageValue) {
		if curContentState != next {
			endContentState()
			curContentState = next
			ch <- messages.StreamMessage{Type: startType, ActorProvidedIndex: defaultIndex, Value: startValue}
		}
	}

	scanner := bufio.NewScanner(reader)
	// Default bufio.Scanner buffer is 64KB which can truncate large SSE payloads
	// (e.g. tool call arguments with big JSON or base64 audio chunks).
	// Use a 1MB buffer to handle large payloads without silent truncation.
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Capture usage (present in last chunk when stream_options.include_usage is set)
		if chunk.Usage != nil {
			lastUsage = messages.TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
			if chunk.Usage.CompletionTokensDetails != nil {
				lastUsage.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		finishReason := chunk.Choices[0].FinishReason

		// Reasoning tokens (OpenRouter / DeepInfra thinking tokens via delta.reasoning)
		if delta.Reasoning != "" {
			transitionTo(contentStateReasoning, messages.StreamTypeReasoningStart, messages.NewReasoningStartValue())
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeReasoningDelta,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewReasoningDeltaValue(delta.Reasoning),
			}
		}

		// Audio output (delta.audio.data is base64-encoded PCM)
		if delta.Audio != nil && delta.Audio.Data != "" {
			decoded, err := base64.StdEncoding.DecodeString(delta.Audio.Data)
			if err == nil && len(decoded) > 0 {
				transitionTo(contentStateAudio, messages.StreamTypeAudioStart, messages.NewAudioStartValue())
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeAudioDelta,
					ActorProvidedIndex: defaultIndex,
					Value:              messages.NewAudioDeltaValue(decoded),
				}
			}
		}

		// Refusal deltas: accumulate silently, emit once after stream ends.
		if delta.Refusal != "" {
			refusalBuf.WriteString(delta.Refusal)
		}

		// Text content
		if delta.Content != "" {
			transitionTo(contentStateText, messages.StreamTypeTextStart, messages.NewTextStartValue())
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeTextDelta,
				ActorProvidedIndex: defaultIndex,
				Value:              messages.NewTextDeltaValue(delta.Content),
			}
		}

		// Tool calls: streamed as repeated deltas per index (id/name first, then arguments appended)
		if len(delta.ToolCalls) > 0 && curContentState != contentStateToolCall {
			endContentState()
			curContentState = contentStateToolCall
		}
		for _, tc := range delta.ToolCalls {
			idx := tc.Index
			acc, exists := toolCalls[idx]
			if !exists {
				acc = struct{ id, name, args string }{id: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments}
				toolCalls[idx] = acc
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeToolCallStart,
					ActorProvidedIndex: idx,
					Value:              messages.NewToolCallStartValue(tc.ID, tc.Function.Name),
				}
			} else {
				acc.args += tc.Function.Arguments
				toolCalls[idx] = acc
			}
			if tc.Function.Arguments != "" {
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeToolCallDelta,
					ActorProvidedIndex: idx,
					Value:              messages.NewToolCallDeltaValue(tc.Function.Arguments),
				}
			}
		}

		// On finish_reason, close the current content block but do NOT return:
		// the API may send additional chunks (e.g. a usage-only chunk) before [DONE].
		switch finishReason {
		case "stop", "length", "content_filter":
			endContentState()
		case "tool_calls":
			for idx, acc := range toolCalls {
				if toolCallEnded[idx] {
					continue
				}
				toolCallEnded[idx] = true
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeToolCallEnd,
					ActorProvidedIndex: idx,
					Value:              messages.NewToolCallEndValue(acc.id, acc.name, acc.args),
				}
			}
			endContentState()
		}
	}

	// Stream ended without explicit finish_reason (connection close, context cancellation, etc.)
	endContentState()
	for idx, acc := range toolCalls {
		if !toolCallEnded[idx] {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeToolCallEnd,
				ActorProvidedIndex: idx,
				Value:              messages.NewToolCallEndValue(acc.id, acc.name, acc.args),
			}
		}
	}
	sendMessageEnd()

	if err := scanner.Err(); err != nil {
		streamErr := gateway.NewTransportError("openai", "chat completions stream", err)
		if cancellationErr := gateway.CancellationErrorOrNil("openai: chat completions stream cancelled", err); cancellationErr != nil {
			streamErr = cancellationErr
		}
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeError,
			ActorProvidedIndex: 0,
			Value:              messages.NewErrorValueWithError(streamErr),
		}
	}
}
