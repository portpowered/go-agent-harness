package participants

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func (r *ModelRunner) runInference(ctx context.Context) error {
	for {
		req, ok := r.Inbox.ReadBlocking(ctx.Done())
		if !ok {
			return ctx.Err()
		}

		execCtx, execCancel := context.WithCancel(ctx)
		r.execMu.Lock()
		r.execCancel = execCancel
		r.execMu.Unlock()

		r.streamID = mustStreamID("model")
		r.actorIndex = 0
		r.currentPassID = req.LoopPassID

		streamCh, err := r.inferencer.InferStream(execCtx, req)
		if isCancellationError(err) {
			r.emitSyntheticDeltas(ctx, messages.InferenceResult{}, err)
		} else if err == nil && streamCh != nil {
			r.drainStream(ctx, execCtx, streamCh)
		} else {
			// The inference provider does not support streaming; fall back to non-streaming
			// and emit synthetic deltas so the ordering layer sees the same delta boundary.
			result, inferErr := r.inferencer.Infer(execCtx, req)
			r.emitSyntheticDeltas(ctx, result, inferErr)
		}

		r.execMu.Lock()
		r.execCancel = nil
		r.execMu.Unlock()
		execCancel()
	}
}

// writeDelta assigns runner-specific ordering (ActorStreamID, ActorProvidedIndex, ActorProvidedID, ActorID, LoopPassID) and writes to DeltaOutbox.
func (r *ModelRunner) writeDelta(ctx context.Context, sm messages.StreamMessage) {
	sm.ActorStreamID = r.streamID
	sm.ActorProvidedIndex = r.actorIndex
	sm.ActorProvidedID = fmt.Sprintf("model-%s-%d", r.streamID, r.actorIndex)
	sm.ActorID = messages.Model
	sm.LoopPassID = r.currentPassID
	r.actorIndex++
	r.DeltaOutbox.Write(ctx, sm)
}

// drainStream forwards each StreamMessage from the inferencer channel to DeltaOutbox.
// It stops on MESSAGE.END, terminal ERROR, or channel close (emitting a synthetic
// MESSAGE.END in the latter case so the ordering layer always sees a complete message
// boundary). Nonterminal ERROR diagnostics are forwarded and do not stop the stream.
func (r *ModelRunner) drainStream(writeCtx, execCtx context.Context, ch <-chan messages.StreamMessage) {
	hasOutput := false
	for {
		if err := execCtx.Err(); err != nil {
			r.writeDelta(writeCtx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Role:  messages.RoleAssistant,
				Value: cancellationErrorValue(err, messages.TerminalProvenanceLoop, outputState(hasOutput)),
			})
			return
		}
		select {
		case <-writeCtx.Done():
			return
		case <-execCtx.Done():
			r.writeDelta(writeCtx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Role:  messages.RoleAssistant,
				Value: cancellationErrorValue(execCtx.Err(), messages.TerminalProvenanceLoop, outputState(hasOutput)),
			})
			return
		case msg, ok := <-ch:
			if !ok {
				if err := execCtx.Err(); err != nil {
					r.writeDelta(writeCtx, messages.StreamMessage{
						Type:  messages.StreamTypeError,
						Role:  messages.RoleAssistant,
						Value: cancellationErrorValue(err, messages.TerminalProvenanceLoop, outputState(hasOutput)),
					})
					return
				}
				// Channel closed without MESSAGE.END; emit a provider-close end so
				// callers can distinguish transport close from clean completion.
				r.writeDelta(writeCtx, messages.StreamMessage{
					Type: messages.StreamTypeMessageEnd,
					Role: messages.RoleAssistant,
					Value: messages.NewMessageEndValueWithTerminal(
						messages.TokenUsage{},
						messages.TerminalReasonProviderClose,
						messages.TerminalProvenanceProvider,
						outputState(hasOutput),
					),
				})
				return
			}
			msg = normalizeProviderTerminalMessage(msg, hasOutput)
			r.writeDelta(writeCtx, msg)
			if isOutputDelta(msg) {
				hasOutput = true
			}
			switch value := msg.Value.(type) {
			case *messages.MessageEndValue:
				return
			case *messages.ErrorValue:
				if value.IsTerminal() {
					return
				}
			}
		}
	}
}

// emitSyntheticDeltas converts a non-streaming InferenceResult into the same sequence
// of deltas that a streaming response would produce, so the ordering layer's assembly
// path is identical regardless of whether the inferencer supports streaming.
func (r *ModelRunner) emitSyntheticDeltas(ctx context.Context, result messages.InferenceResult, inferErr error) {
	if inferErr != nil {
		if isCancellationError(inferErr) {
			r.writeDelta(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: cancellationErrorValue(inferErr, messages.TerminalProvenanceLoop, messages.TerminalOutputNone),
			})
			return
		}
		r.writeDelta(ctx, messages.StreamMessage{
			Type: messages.StreamTypeError,
			Value: messages.NewErrorValueWithTerminal(
				inferErr.Error(),
				"",
				messages.TerminalReasonTerminalFailure,
				messages.TerminalProvenanceLoop,
				messages.TerminalOutputNone,
			),
		})
		return
	}

	r.writeDelta(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})

	// Emit text content.
	if text := result.Message.TextContent(); result.Message.HasText() {
		r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()})
		if text != "" {
			r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)})
		}
		r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()})
	}

	// Emit tool call deltas so the ordering layer can assemble ToolCalls on the message.
	for _, tc := range result.ToolCalls {
		r.writeDelta(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewToolCallStartValue(tc.ID, tc.Name),
		})
		r.writeDelta(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments),
		})
	}

	// Emit multimodal content parts.
	for _, part := range result.Message.ContentParts {
		switch p := part.(type) {
		case messages.AudioPart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()})
			}
		case messages.ImagePart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeImageStart, Value: messages.NewImageStartValue(p.MediaType)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeImageDelta, Value: messages.NewImageDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeImageEnd, Value: messages.NewImageEndValue()})
			}
		case messages.VideoPart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeVideoStart, Value: messages.NewVideoStartValue(p.MediaType)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeVideoDelta, Value: messages.NewVideoDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeVideoEnd, Value: messages.NewVideoEndValue()})
			}
		case messages.FilePart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeFileStart, Value: messages.NewFileStartValue(p.MediaType, p.Name)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeFileDelta, Value: messages.NewFileDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeFileEnd, Value: messages.NewFileEndValue()})
			}
		}
	}

	// Emit token usage information if present.
	if result.TokenUsage.PromptTokens != 0 || result.TokenUsage.CompletionTokens != 0 || result.TokenUsage.TotalTokens != 0 || result.TokenUsage.ReasoningTokens != 0 {
		r.writeDelta(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeUsageInfo,
			Value: messages.NewUsageInfoValue(result.TokenUsage),
		})
	}

	r.writeDelta(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewSynthesizedMessageEndValue(result.TokenUsage),
	})
}

func normalizeProviderTerminalMessage(msg messages.StreamMessage, hasOutput bool) messages.StreamMessage {
	switch value := msg.Value.(type) {
	case *messages.MessageEndValue:
		if value.TerminalReason == "" {
			value.TerminalReason = messages.TerminalReasonProviderAuthoredCompletion
		}
		if value.TerminalProvenance == "" {
			value.TerminalProvenance = messages.TerminalProvenanceProvider
		}
		if value.OutputState == "" {
			value.OutputState = messages.TerminalOutputComplete
		}
	case *messages.ErrorValue:
		if value.IsNonTerminal() {
			break
		}
		if value.TerminalReason == "" {
			value.TerminalReason = messages.TerminalReasonTerminalFailure
		}
		if value.TerminalProvenance == "" {
			value.TerminalProvenance = messages.TerminalProvenanceProvider
		}
		if value.OutputState == "" {
			value.OutputState = outputState(hasOutput)
		}
	}
	return msg
}

func outputState(hasOutput bool) messages.TerminalOutputState {
	if hasOutput {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNone
}

// hasPCM16Signal distinguishes a real input frame from the zero-filled
// cadence frames produced by a room mixer while no participant is speaking.
// The frame is still forwarded in either case so the provider's audio timing
// and VAD state remain intact; only a frame with at least one non-zero byte can
// be the user activity that cancels an in-flight response.
func cancellationErrorValue(err error, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) *messages.ErrorValue {
	if err == nil {
		err = context.Canceled
	}
	value := messages.NewErrorValueWithTerminal(
		err.Error(),
		string(messages.TerminalReasonCancellation),
		messages.TerminalReasonCancellation,
		provenance,
		outputState,
	)
	value.Err = err
	return value
}

func isCancellationError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isOutputDelta(msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeRefusal:
		return true
	default:
		return false
	}
}

func isCustomerOutputDelta(msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeRefusal:
		return true
	default:
		// Tool-call deltas remain visible after a speech cancellation so the
		// tool lifecycle can resolve or reject the outstanding call explicitly.
		return false
	}
}
