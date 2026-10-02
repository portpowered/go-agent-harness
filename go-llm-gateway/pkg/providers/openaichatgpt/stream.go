package openaichatgpt

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	// sseLineBytes bounds one SSE line (a large tool-call argument arrives as
	// one data line).
	sseLineBytes  = 1 << 20
	sseDataPrefix = "data:"
	sseDoneMarker = "[DONE]"
	textIndex     = 0
)

// Responses stream event types this translator reads. Every other type
// (response.created, response.in_progress, content_part.*, *.done text
// events, ...) carries nothing the gateway needs and is skipped, as in
// Codex's process_responses_event.
const (
	eventOutputTextDelta       = "response.output_text.delta"
	eventRefusalDelta          = "response.refusal.delta"
	eventReasoningSummaryDelta = "response.reasoning_summary_text.delta"
	eventReasoningTextDelta    = "response.reasoning_text.delta"
	eventOutputItemAdded       = "response.output_item.added"
	eventOutputItemDone        = "response.output_item.done"
	eventArgumentsDelta        = "response.function_call_arguments.delta"
	eventCompleted             = "response.completed"
	eventDone                  = "response.done"
	eventIncomplete            = "response.incomplete"
	eventFailed                = "response.failed"
	eventError                 = "error"
)

// ErrStreamEnded reports a stream that closed before response.completed.
var ErrStreamEnded = errors.New("stream ended before response.completed")

// ErrUnresolvedToolCall reports a function call that was announced but never
// completed before the response ended.
var ErrUnresolvedToolCall = errors.New("stream ended with an unfinished function call")

type streamEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta"`
	OutputIndex *int            `json:"output_index"`
	ItemID      string          `json:"item_id"`
	Item        *outputItem     `json:"item"`
	Response    *responseObject `json:"response"`
	Code        string          `json:"code"`
	Error       *apiError       `json:"error"`
}

type outputItem struct {
	Type      string       `json:"type"`
	ID        string       `json:"id"`
	CallID    string       `json:"call_id"`
	Name      string       `json:"name"`
	Arguments string       `json:"arguments"`
	Content   []outputPart `json:"content"`
}

type outputPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responseObject struct {
	Usage             *responseUsage `json:"usage"`
	Error             *apiError      `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

// responseUsage is response.usage (Codex ResponseCompletedUsage,
// codex-rs/codex-api/src/sse/responses.rs).
type responseUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	TotalTokens         int `json:"total_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type blockKind int

const (
	blockNone blockKind = iota
	blockText
	blockReasoning
)

type toolCallState struct {
	callID, name, args string
	ended              bool
}

// streamState translates one Responses stream into gateway stream messages.
type streamState struct {
	ch        chan<- messages.StreamMessage
	open      blockKind
	textItems map[string]bool // message items whose text arrived as deltas
	// untrackedText marks deltas without an item id since the last message
	// item completed.
	untrackedText bool
	toolCalls     map[int]*toolCallState
	toolOrder     []int
	usage         messages.TokenUsage
	refusal       strings.Builder
	terminated    bool
	err           error
}

// translateStream reads the SSE body, emits MESSAGE.START, the content
// messages, and then MESSAGE.END (and USAGE.INFO when usage is known). A
// failure is emitted as ERROR before MESSAGE.END. closeBody runs before
// MESSAGE.END so a recording transport is flushed first.
func translateStream(body io.Reader, ch chan<- messages.StreamMessage, closeBody func() error) {
	state := &streamState{ch: ch, textItems: map[string]bool{}, toolCalls: map[int]*toolCallState{}}
	state.emit(messages.StreamTypeMessageStart, messages.NewMessageStartValue())
	readErr := readSSE(body, state.handleData)
	state.finish(readErr)
	if err := closeBody(); err != nil && state.err == nil {
		state.err = transportError("close responses stream", err)
	}
	if state.err != nil {
		state.emit(messages.StreamTypeError, providers.NewStreamErrorValue(state.err))
	}
	if state.refusal.Len() > 0 {
		state.emit(messages.StreamTypeRefusal, messages.NewRefusalValue(state.refusal.String()))
	}
	state.emit(messages.StreamTypeMessageEnd, messages.NewMessageEndValue(state.usage))
	if state.usage != (messages.TokenUsage{}) {
		state.emit(messages.StreamTypeUsageInfo, messages.NewUsageInfoValue(state.usage))
	}
}

// readSSE calls handle with the data of each SSE event (its data lines
// joined) until handle reports the end or the body ends.
func readSSE(body io.Reader, handle func(string) bool) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, sseLineBytes), sseLineBytes)
	var data []string
	flush := func() bool {
		if len(data) == 0 {
			return false
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		return handle(payload)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if flush() {
				return nil
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, sseDataPrefix); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

// handleData applies one event and reports whether the stream is over.
func (s *streamState) handleData(data string) bool {
	if data == sseDoneMarker || strings.TrimSpace(data) == "" {
		return data == sseDoneMarker
	}
	var event streamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		s.err = &providers.ProviderError{Provider: ProviderName, Detail: "malformed stream event", Err: providers.ErrProviderRejected}
		return true
	}
	s.apply(event)
	return s.terminated || s.err != nil
}

func (s *streamState) apply(event streamEvent) {
	switch event.Type {
	case eventOutputTextDelta:
		s.textDelta(event.ItemID, event.Delta)
	case eventRefusalDelta:
		s.refusal.WriteString(event.Delta)
	case eventReasoningSummaryDelta, eventReasoningTextDelta:
		s.transition(blockReasoning)
		s.emit(messages.StreamTypeReasoningDelta, messages.NewReasoningDeltaValue(event.Delta))
	case eventOutputItemAdded:
		if event.Item != nil && event.Item.Type == itemTypeFunctionCall {
			s.startToolCall(s.indexOf(event), event.Item)
		}
	case eventArgumentsDelta:
		s.argumentsDelta(s.indexOf(event), event.Delta)
	case eventOutputItemDone:
		if event.Item != nil {
			s.itemDone(s.indexOf(event), event.Item)
		}
	case eventCompleted, eventDone:
		s.complete(event.Response)
	case eventIncomplete:
		s.incomplete(event.Response)
	case eventFailed:
		var failure *apiError
		if event.Response != nil {
			failure = event.Response.Error
		}
		s.err = streamFailure(eventFailed, failure)
	case eventError:
		failure := event.Error
		if failure == nil {
			failure = &apiError{Code: event.Code}
		}
		s.err = streamFailure(eventError, failure)
	}
}

func (s *streamState) indexOf(event streamEvent) int {
	if event.OutputIndex != nil {
		return *event.OutputIndex
	}
	return len(s.toolOrder)
}

func (s *streamState) textDelta(itemID, delta string) {
	if itemID != "" {
		s.textItems[itemID] = true
	} else {
		s.untrackedText = true
	}
	s.transition(blockText)
	s.emit(messages.StreamTypeTextDelta, messages.NewTextDeltaValue(delta))
}

func (s *streamState) startToolCall(index int, item *outputItem) {
	if _, seen := s.toolCalls[index]; seen {
		return
	}
	s.transition(blockNone)
	call := &toolCallState{callID: item.CallID, name: item.Name, args: item.Arguments}
	s.toolCalls[index] = call
	s.toolOrder = append(s.toolOrder, index)
	s.ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: index, Value: messages.NewToolCallStartValue(call.callID, call.name)}
	if call.args != "" {
		s.ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: index, Value: messages.NewToolCallDeltaValue(call.args)}
	}
}

func (s *streamState) argumentsDelta(index int, delta string) {
	call, ok := s.toolCalls[index]
	if !ok || call.ended || delta == "" {
		return
	}
	call.args += delta
	s.ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: index, Value: messages.NewToolCallDeltaValue(delta)}
}

// itemDone completes an output item. A function_call's done item is
// authoritative for its id, name and arguments; a message's done item
// supplies its text when no deltas carried it (Codex reads output text from
// the done item).
func (s *streamState) itemDone(index int, item *outputItem) {
	switch item.Type {
	case itemTypeFunctionCall:
		s.startToolCall(index, item)
		call := s.toolCalls[index]
		if call.ended {
			return
		}
		call.ended = true
		if item.CallID != "" {
			call.callID = item.CallID
		}
		if item.Name != "" {
			call.name = item.Name
		}
		if item.Arguments != "" {
			call.args = item.Arguments
		}
		s.ch <- messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: index, Value: messages.NewToolCallEndValue(call.callID, call.name, call.args)}
	case itemTypeMessage:
		s.messageDone(item)
	case itemTypeReasoning:
		if s.open == blockReasoning {
			s.transition(blockNone)
		}
	}
}

func (s *streamState) messageDone(item *outputItem) {
	streamed := s.untrackedText || (item.ID != "" && s.textItems[item.ID])
	s.untrackedText = false
	for _, part := range item.Content {
		switch part.Type {
		case partTypeOutputText:
			if !streamed && part.Text != "" {
				s.transition(blockText)
				s.emit(messages.StreamTypeTextDelta, messages.NewTextDeltaValue(part.Text))
			}
		case partTypeRefusal:
			if !streamed {
				s.refusal.WriteString(part.Refusal)
			}
		}
	}
	if s.open == blockText {
		s.transition(blockNone)
	}
}

func (s *streamState) complete(response *responseObject) {
	s.terminated = true
	if response == nil || response.Usage == nil {
		return
	}
	usage := response.Usage
	s.usage = messages.TokenUsage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}
	if usage.OutputTokensDetails != nil {
		s.usage.ReasoningTokens = usage.OutputTokensDetails.ReasoningTokens
	}
}

// incomplete fails the turn as Codex does ("Incomplete response returned,
// reason: ..."); the reason is a short backend enum.
func (s *streamState) incomplete(response *responseObject) {
	reason := "unknown"
	if response != nil && response.IncompleteDetails != nil {
		if code := sanitizedCode(response.IncompleteDetails.Reason); code != "" {
			reason = code
		}
	}
	s.err = &providers.ProviderError{Provider: ProviderName, Detail: "incomplete response: " + reason, Err: providers.ErrProviderRejected}
}

// finish closes open blocks and records why the stream ended badly, if it
// did.
func (s *streamState) finish(readErr error) {
	s.transition(blockNone)
	switch {
	case s.err != nil:
	case readErr != nil:
		if cancelled := gateway.CancellationErrorOrNil(ProviderName+": responses stream cancelled", readErr); cancelled != nil {
			s.err = cancelled
		} else {
			s.err = transportError("responses stream", readErr)
		}
	case !s.terminated:
		s.err = transportError("responses stream", ErrStreamEnded)
	default:
		for _, index := range s.toolOrder {
			if !s.toolCalls[index].ended {
				s.err = &providers.ProviderError{Provider: ProviderName, Detail: ErrUnresolvedToolCall.Error(), Err: errors.Join(providers.ErrProviderRejected, ErrUnresolvedToolCall)}
				return
			}
		}
	}
}

// transition ends the open text or reasoning block and opens next.
func (s *streamState) transition(next blockKind) {
	if s.open == next {
		return
	}
	switch s.open {
	case blockText:
		s.emit(messages.StreamTypeTextEnd, messages.NewTextEndValue())
	case blockReasoning:
		s.emit(messages.StreamTypeReasoningEnd, messages.NewReasoningEndValue())
	case blockNone:
	}
	s.open = next
	switch next {
	case blockText:
		s.emit(messages.StreamTypeTextStart, messages.NewTextStartValue())
	case blockReasoning:
		s.emit(messages.StreamTypeReasoningStart, messages.NewReasoningStartValue())
	case blockNone:
	}
}

func (s *streamState) emit(messageType messages.StreamMessageType, value messages.StreamMessageValue) {
	s.ch <- messages.StreamMessage{Type: messageType, ActorProvidedIndex: textIndex, Value: value}
}
