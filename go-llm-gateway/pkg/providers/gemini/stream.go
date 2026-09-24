package gemini

import (
	"encoding/json"

	"google.golang.org/genai"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const defaultStreamIndex = 0

// streamIterator matches iter.Seq2[*genai.GenerateContentResponse, error] returned
// by client.Models.GenerateContentStream, allowing mock injection for tests.
type streamIterator = func(yield func(*genai.GenerateContentResponse, error) bool)

// streamGeminiToGateway reads Gemini stream responses and sends gateway StreamMessages.
// Emits MESSAGE.START, then content blocks (TEXT.*, TOOLCALL.*), then MESSAGE.END with usage.
// Caller must not close ch; streamGeminiToGateway closes it when the stream ends.
func streamGeminiToGateway(iter streamIterator, ch chan<- messages.StreamMessage) {
	defer close(ch)

	state := &geminiStreamState{ch: ch}
	var streamErr error

	iter(func(resp *genai.GenerateContentResponse, err error) bool {
		if err != nil {
			streamErr = err
			return false
		}
		state.handleResponse(resp)
		return true
	})

	// Close any open text block.
	state.endTextBlock()

	// Ensure MESSAGE.START was sent even if the stream yielded nothing useful.
	state.sendMessageStart()

	// Send error before MESSAGE.END so consumers can handle partial responses.
	if streamErr != nil {
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeError,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              providers.NewStreamTransportErrorValue(streamErr),
		}
	}

	state.sendMessageEnd()
}

// geminiStreamState tracks message framing, the open text block, tool-call
// ordinals, and usage while one Gemini stream is translated.
type geminiStreamState struct {
	ch             chan<- messages.StreamMessage
	inText         bool
	toolCallIndex  int
	lastUsage      messages.TokenUsage
	messageStarted bool
	messageEndSent bool
}

// handleResponse translates one successful stream chunk.
func (s *geminiStreamState) handleResponse(resp *genai.GenerateContentResponse) {
	s.sendMessageStart()

	// Extract usage metadata (typically present on last chunk).
	if resp.UsageMetadata != nil {
		prompt := resp.UsageMetadata.PromptTokenCount
		completion := resp.UsageMetadata.CandidatesTokenCount
		s.lastUsage = messages.TokenUsage{
			PromptTokens:     int(prompt),
			CompletionTokens: int(completion),
			TotalTokens:      int(prompt + completion),
		}
	}

	if len(resp.Candidates) == 0 {
		return
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil {
		return
	}

	for _, part := range candidate.Content.Parts {
		// Text content
		if part.Text != "" {
			s.emitText(part.Text)
		}

		// Function call (tool call) — Gemini sends complete FunctionCall parts per chunk.
		if part.FunctionCall != nil {
			s.endTextBlock()
			s.emitFunctionCall(part.FunctionCall)
		}
	}
}

func (s *geminiStreamState) emitText(text string) {
	if !s.inText {
		s.inText = true
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeTextStart,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              messages.NewTextStartValue(),
		}
	}
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeTextDelta,
		ActorProvidedIndex: defaultStreamIndex,
		Value:              messages.NewTextDeltaValue(text),
	}
}

func (s *geminiStreamState) emitFunctionCall(fc *genai.FunctionCall) {
	argsJSON, jsonErr := json.Marshal(fc.Args)
	if jsonErr != nil {
		argsJSON = []byte("{}")
	}
	args := string(argsJSON)

	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeToolCallStart,
		ActorProvidedIndex: s.toolCallIndex,
		Value:              messages.NewToolCallStartValue(fc.ID, fc.Name),
	}
	if args != "" && args != "null" {
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeToolCallDelta,
			ActorProvidedIndex: s.toolCallIndex,
			Value:              messages.NewToolCallDeltaValue(args),
		}
	}
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeToolCallEnd,
		ActorProvidedIndex: s.toolCallIndex,
		Value:              messages.NewToolCallEndValue(fc.ID, fc.Name, args),
	}
	s.toolCallIndex++
}

func (s *geminiStreamState) sendMessageStart() {
	if s.messageStarted {
		return
	}
	s.messageStarted = true
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageStart,
		ActorProvidedIndex: defaultStreamIndex,
		Value:              messages.NewMessageStartValue(),
	}
}

func (s *geminiStreamState) sendMessageEnd() {
	if s.messageEndSent {
		return
	}
	s.messageEndSent = true
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeMessageEnd,
		ActorProvidedIndex: defaultStreamIndex,
		Value:              messages.NewMessageEndValue(s.lastUsage),
	}
	if s.lastUsage.PromptTokens != 0 || s.lastUsage.CompletionTokens != 0 || s.lastUsage.TotalTokens != 0 {
		s.ch <- messages.StreamMessage{
			Type:               messages.StreamTypeUsageInfo,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              messages.NewUsageInfoValue(s.lastUsage),
		}
	}
}

func (s *geminiStreamState) endTextBlock() {
	if !s.inText {
		return
	}
	s.inText = false
	s.ch <- messages.StreamMessage{
		Type:               messages.StreamTypeTextEnd,
		ActorProvidedIndex: defaultStreamIndex,
		Value:              messages.NewTextEndValue(),
	}
}
