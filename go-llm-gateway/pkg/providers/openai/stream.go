package openai

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
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

const (
	// sseStreamDefaultIndex is the actor-provided index of non-tool events.
	sseStreamDefaultIndex = 0
	// sseScannerBufferBytes bounds one SSE line. The default bufio.Scanner
	// buffer is 64KB, which can truncate large SSE payloads (e.g. tool call
	// arguments with big JSON or base64 audio chunks), so use 1MB to handle
	// large payloads without silent truncation.
	sseScannerBufferBytes = 1 << 20
	sseDataPrefix         = "data: "
	sseDoneMarker         = "[DONE]"
)

// sseToolCallAccumulator accumulates one streamed tool call by index.
type sseToolCallAccumulator struct{ id, name, args string }

// sseStreamState owns the per-stream mapping state for streamSSEToGateway.
type sseStreamState struct {
	ch              chan<- messages.StreamMessage
	curContentState contentState
	toolCalls       map[int]sseToolCallAccumulator
	toolCallEnded   map[int]bool
	lastUsage       messages.TokenUsage
	messageEndSent  bool
	refusalBuf      strings.Builder // accumulates delta.refusal chunks
}

// streamSSEToGateway reads an OpenAI SSE stream from reader and emits gateway StreamMessages on ch.
// It parses SSE data lines, decodes JSON chunks, and maps them to typed events:
// MESSAGE.START, TEXT.*, AUDIO.*, REASONING.*, TOOLCALL.*, USAGE_INFO, MESSAGE.END.
// Supports delta.reasoning for OpenRouter/DeepInfra thinking tokens and delta.audio for audio output.
// When closeBody is provided, it must complete before MESSAGE.END so callers
// can flush a recorder only after the HTTP response body is inactive.
func streamSSEToGateway(reader io.Reader, ch chan<- messages.StreamMessage, closeBody ...func() error) {
	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageStart,
		ActorProvidedIndex: sseStreamDefaultIndex,
		Value:              messages.NewMessageStartValue(),
	}

	state := &sseStreamState{
		ch:              ch,
		curContentState: contentStateNone,
		toolCalls:       make(map[int]sseToolCallAccumulator),
		toolCallEnded:   make(map[int]bool),
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, sseScannerBufferBytes), sseScannerBufferBytes)
	for scanner.Scan() {
		if state.handleLine(scanner.Text()) {
			break
		}
	}

	// Stream ended without explicit finish_reason (connection close, context cancellation, etc.)
	state.finishOpenContent()
	scanErr := scanner.Err()
	closeResponseBody(closeBody, ch, state.sendMessageEnd)
	if scanErr != nil {
		emitSSEScanError(ch, scanErr)
	}
}

// handleLine maps one SSE line and reports whether the [DONE] marker ended the stream.
func (s *sseStreamState) handleLine(line string) bool {
	if !strings.HasPrefix(line, sseDataPrefix) {
		return false
	}
	data := strings.TrimPrefix(line, sseDataPrefix)
	if data == sseDoneMarker {
		return true
	}

	var chunk streamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false
	}
	s.applyChunk(chunk)
	return false
}

func (s *sseStreamState) applyChunk(chunk streamChunk) {
	// Capture usage (present in last chunk when stream_options.include_usage is set)
	if chunk.Usage != nil {
		s.lastUsage = messages.TokenUsage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
		if chunk.Usage.CompletionTokensDetails != nil {
			s.lastUsage.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
		}
	}

	if len(chunk.Choices) == 0 {
		return
	}
	s.applyDelta(chunk.Choices[0].Delta)
	s.applyFinishReason(chunk.Choices[0].FinishReason)
}

func (s *sseStreamState) applyDelta(delta streamDelta) {
	// Reasoning tokens (OpenRouter / DeepInfra thinking tokens via delta.reasoning)
	if delta.Reasoning != "" {
		s.transitionTo(contentStateReasoning, messages.StreamTypeReasoningStart, messages.NewReasoningStartValue())
		s.emit(messages.StreamTypeReasoningDelta, messages.NewReasoningDeltaValue(delta.Reasoning))
	}

	// Audio output (delta.audio.data is base64-encoded PCM)
	if delta.Audio != nil && delta.Audio.Data != "" {
		decoded, err := codec.DecodeBase64(delta.Audio.Data)
		if err == nil && len(decoded) > 0 {
			s.transitionTo(contentStateAudio, messages.StreamTypeAudioStart, messages.NewAudioStartValue())
			s.emit(messages.StreamTypeAudioDelta, messages.NewAudioDeltaValue(decoded))
		}
	}

	// Refusal deltas: accumulate silently, emit once after stream ends.
	if delta.Refusal != "" {
		s.refusalBuf.WriteString(delta.Refusal)
	}

	// Text content
	if delta.Content != "" {
		s.transitionTo(contentStateText, messages.StreamTypeTextStart, messages.NewTextStartValue())
		s.emit(messages.StreamTypeTextDelta, messages.NewTextDeltaValue(delta.Content))
	}

	s.applyToolCallDeltas(delta.ToolCalls)
}

// applyToolCallDeltas maps tool calls streamed as repeated deltas per index
// (id/name first, then arguments appended).
func (s *sseStreamState) applyToolCallDeltas(toolCalls []streamToolCall) {
	if len(toolCalls) > 0 && s.curContentState != contentStateToolCall {
		s.endContentState()
		s.curContentState = contentStateToolCall
	}
	for _, tc := range toolCalls {
		idx := tc.Index
		acc, exists := s.toolCalls[idx]
		if !exists {
			acc = sseToolCallAccumulator{id: tc.ID, name: tc.Function.Name, args: tc.Function.Arguments}
			s.toolCalls[idx] = acc
			s.ch <- messages.StreamMessage{
				Type:               messages.StreamTypeToolCallStart,
				ActorProvidedIndex: idx,
				Value:              messages.NewToolCallStartValue(tc.ID, tc.Function.Name),
			}
		} else {
			acc.args += tc.Function.Arguments
			s.toolCalls[idx] = acc
		}
		if tc.Function.Arguments != "" {
			s.ch <- messages.StreamMessage{
				Type:               messages.StreamTypeToolCallDelta,
				ActorProvidedIndex: idx,
				Value:              messages.NewToolCallDeltaValue(tc.Function.Arguments),
			}
		}
	}
}

// applyFinishReason closes the current content block on finish_reason but does
// not end the stream: the API may send additional chunks (e.g. a usage-only
// chunk) before [DONE].
func (s *sseStreamState) applyFinishReason(finishReason string) {
	switch finishReason {
	case "stop", "length", "content_filter":
		s.endContentState()
	case "tool_calls":
		for idx, acc := range s.toolCalls {
			if s.toolCallEnded[idx] {
				continue
			}
			s.toolCallEnded[idx] = true
			s.emitToolCallEnd(idx, acc)
		}
		s.endContentState()
	}
}

// finishOpenContent ends open content and emits TOOLCALL.END for tool calls
// that never received one.
func (s *sseStreamState) finishOpenContent() {
	s.endContentState()
	for idx, acc := range s.toolCalls {
		if !s.toolCallEnded[idx] {
			s.emitToolCallEnd(idx, acc)
		}
	}
}

func (s *sseStreamState) sendMessageEnd() {
	if s.messageEndSent {
		return
	}
	// Emit accumulated refusal (if any) before MESSAGE.END.
	if s.refusalBuf.Len() > 0 {
		s.emit(messages.StreamTypeRefusal, messages.NewRefusalValue(s.refusalBuf.String()))
	}
	s.messageEndSent = true
	s.emit(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(s.lastUsage))
	usage := s.lastUsage
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 || usage.ReasoningTokens != 0 {
		s.emit(messages.StreamTypeUsageInfo, messages.NewUsageInfoValue(usage))
	}
}

// endContentState emits the appropriate END event for the current content type.
func (s *sseStreamState) endContentState() {
	switch s.curContentState {
	case contentStateNone:
	case contentStateReasoning:
		s.emit(messages.StreamTypeReasoningEnd, messages.NewReasoningEndValue())
	case contentStateText:
		s.emit(messages.StreamTypeTextEnd, messages.NewTextEndValue())
	case contentStateAudio:
		s.emit(messages.StreamTypeAudioEnd, messages.NewAudioEndValue())
	case contentStateToolCall:
		for idx, acc := range s.toolCalls {
			if !s.toolCallEnded[idx] {
				s.toolCallEnded[idx] = true
				s.emitToolCallEnd(idx, acc)
			}
		}
	}
	s.curContentState = contentStateNone
}

// transitionTo switches to a new content type: ends current if different, then starts new.
func (s *sseStreamState) transitionTo(next contentState, startType messages.StreamMessageType, startValue messages.StreamMessageValue) {
	if s.curContentState != next {
		s.endContentState()
		s.curContentState = next
		s.emit(startType, startValue)
	}
}

func (s *sseStreamState) emit(messageType messages.StreamMessageType, value messages.StreamMessageValue) {
	s.ch <- messages.StreamMessage{Type: messageType, ActorProvidedIndex: sseStreamDefaultIndex, Value: value}
}

func (s *sseStreamState) emitToolCallEnd(idx int, acc sseToolCallAccumulator) {
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeToolCallEnd,
		ActorProvidedIndex: idx,
		Value:              messages.NewToolCallEndValue(acc.id, acc.name, acc.args),
	}
}

func emitSSEScanError(ch chan<- messages.StreamMessage, scanErr error) {
	streamErr := gateway.NewTransportError(openAIProviderName, "chat completions stream", scanErr)
	classification := providers.ErrorClassTransport
	if cancellationErr := gateway.CancellationErrorOrNil("openai: chat completions stream cancelled", scanErr); cancellationErr != nil {
		streamErr = cancellationErr
		classification = providers.ErrorClassCancellation
	}
	errValue := messages.NewErrorValueWithError(streamErr)
	errValue.Classification = classification
	ch <- messages.StreamMessage{
		Type:               messages.StreamTypeError,
		ActorProvidedIndex: 0,
		Value:              errValue,
	}
}

func closeResponseBody(closeBody []func() error, ch chan<- messages.StreamMessage, sendMessageEnd func()) {
	if len(closeBody) == 0 || closeBody[0] == nil {
		sendMessageEnd()
		return
	}
	if err := closeBody[0](); err != nil {
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeError,
			ActorProvidedIndex: 0,
			Value:              providers.NewStreamTransportErrorValue(err),
		}
		return
	}
	sendMessageEnd()
}

// openAIProviderName is the provider name reported in transport errors.
const openAIProviderName = "openai"
