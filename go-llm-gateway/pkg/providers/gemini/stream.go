package gemini

import (
	"encoding/json"

	"google.golang.org/genai"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
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

	var (
		inText         bool
		toolCallIndex  int
		lastUsage      messages.TokenUsage
		messageStarted bool
		messageEndSent bool
		streamErr      error
	)

	sendMessageStart := func() {
		if messageStarted {
			return
		}
		messageStarted = true
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeMessageStart,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              messages.NewMessageStartValue(),
		}
	}

	sendMessageEnd := func() {
		if messageEndSent {
			return
		}
		messageEndSent = true
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeMessageEnd,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              messages.NewMessageEndValue(lastUsage),
		}
		if lastUsage.PromptTokens != 0 || lastUsage.CompletionTokens != 0 || lastUsage.TotalTokens != 0 {
			ch <- messages.StreamMessage{
				Type:               messages.StreamTypeUsageInfo,
				ActorProvidedIndex: defaultStreamIndex,
				Value:              messages.NewUsageInfoValue(lastUsage),
			}
		}
	}

	endTextBlock := func() {
		if !inText {
			return
		}
		inText = false
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeTextEnd,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              messages.NewTextEndValue(),
		}
	}

	iter(func(resp *genai.GenerateContentResponse, err error) bool {
		if err != nil {
			streamErr = err
			return false
		}

		sendMessageStart()

		// Extract usage metadata (typically present on last chunk).
		if resp.UsageMetadata != nil {
			prompt := resp.UsageMetadata.PromptTokenCount
			completion := resp.UsageMetadata.CandidatesTokenCount
			lastUsage = messages.TokenUsage{
				PromptTokens:     int(prompt),
				CompletionTokens: int(completion),
				TotalTokens:      int(prompt + completion),
			}
		}

		if len(resp.Candidates) == 0 {
			return true
		}
		candidate := resp.Candidates[0]
		if candidate.Content == nil {
			return true
		}

		for _, part := range candidate.Content.Parts {
			// Text content
			if part.Text != "" {
				if !inText {
					inText = true
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeTextStart,
						ActorProvidedIndex: defaultStreamIndex,
						Value:              messages.NewTextStartValue(),
					}
				}
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeTextDelta,
					ActorProvidedIndex: defaultStreamIndex,
					Value:              messages.NewTextDeltaValue(part.Text),
				}
			}

			// Function call (tool call) — Gemini sends complete FunctionCall parts per chunk.
			if part.FunctionCall != nil {
				endTextBlock()

				fc := part.FunctionCall
				argsJSON, jsonErr := json.Marshal(fc.Args)
				if jsonErr != nil {
					argsJSON = []byte("{}")
				}
				args := string(argsJSON)

				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeToolCallStart,
					ActorProvidedIndex: toolCallIndex,
					Value:              messages.NewToolCallStartValue(fc.ID, fc.Name),
				}
				if args != "" && args != "null" {
					ch <- messages.StreamMessage{
						Type:               messages.StreamTypeToolCallDelta,
						ActorProvidedIndex: toolCallIndex,
						Value:              messages.NewToolCallDeltaValue(args),
					}
				}
				ch <- messages.StreamMessage{
					Type:               messages.StreamTypeToolCallEnd,
					ActorProvidedIndex: toolCallIndex,
					Value:              messages.NewToolCallEndValue(fc.ID, fc.Name, args),
				}
				toolCallIndex++
			}
		}

		return true
	})

	// Close any open text block.
	endTextBlock()

	// Ensure MESSAGE.START was sent even if the stream yielded nothing useful.
	sendMessageStart()

	// Send error before MESSAGE.END so consumers can handle partial responses.
	if streamErr != nil {
		ch <- messages.StreamMessage{
			Type:               messages.StreamTypeError,
			ActorProvidedIndex: defaultStreamIndex,
			Value:              providers.NewStreamTransportErrorValue(streamErr),
		}
	}

	sendMessageEnd()
}
