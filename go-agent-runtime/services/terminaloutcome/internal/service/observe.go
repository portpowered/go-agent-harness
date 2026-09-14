package service

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

func (r *reporter) ObserveStreamMessage(msg messages.StreamMessage, leadingNewline bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observeStreamMessageLocked(msg, leadingNewline)
}

func (r *reporter) observeStreamMessageLocked(msg messages.StreamMessage, leadingNewline bool) {
	switch {
	case msg.Type == messages.StreamTypeMessageStart:
		r.outcome.outputState = messages.TerminalOutputNone
	case isOutputMessageType(msg.Type):
		if streamMessageHasOutput(msg) {
			r.outcome.outputState = messages.TerminalOutputPartial
		}
	case msg.Type == messages.StreamTypeTranscriptDelta:
		if msg.Role != messages.RoleUser && streamMessageHasOutput(msg) {
			r.outcome.outputState = messages.TerminalOutputPartial
		}
	case msg.Type == messages.StreamTypeMessageEnd:
		if r.outcome.outputState == messages.TerminalOutputPartial {
			r.outcome.outputState = messages.TerminalOutputComplete
		}
	case msg.Type == messages.StreamTypeSessionClose:
		r.observeSessionCloseLocked(msg, leadingNewline)
	case msg.Type == messages.StreamTypeError:
		if value, ok := msg.Value.(*messages.ErrorValue); ok && value != nil && !value.IsNonTerminal() {
			r.observeErrorLocked(value, leadingNewline)
		}
	default:
		// Lifecycle, usage, and content-boundary messages do not alter the
		// terminal output state.
	}
}

func isOutputMessageType(typ messages.StreamMessageType) bool {
	return typ == messages.StreamTypeTextDelta ||
		typ == messages.StreamTypeReasoningDelta ||
		typ == messages.StreamTypeAudioDelta ||
		typ == messages.StreamTypeImageDelta ||
		typ == messages.StreamTypeVideoDelta ||
		typ == messages.StreamTypeFileDelta ||
		typ == messages.StreamTypeEmbeddingDelta ||
		typ == messages.StreamTypeToolCallDelta ||
		typ == messages.StreamTypeToolCallEnd ||
		typ == messages.StreamTypeRefusal
}

func streamMessageHasOutput(msg messages.StreamMessage) bool {
	switch value := msg.Value.(type) {
	case *messages.TextDeltaValue:
		return textDeltaHasOutput(value)
	case *messages.ReasoningDeltaValue:
		return reasoningDeltaHasOutput(value)
	case *messages.AudioDeltaValue:
		return audioDeltaHasOutput(value)
	case *messages.ImageDeltaValue:
		return imageDeltaHasOutput(value)
	case *messages.VideoDeltaValue:
		return videoDeltaHasOutput(value)
	case *messages.FileDeltaValue:
		return fileDeltaHasOutput(value)
	case *messages.EmbeddingDeltaValue:
		return embeddingDeltaHasOutput(value)
	case *messages.ToolCallDeltaValue:
		return toolCallDeltaHasOutput(value)
	case *messages.ToolCallEndValue:
		return value != nil
	case *messages.RefusalValue:
		return refusalHasOutput(value)
	case *messages.TranscriptDeltaValue:
		return transcriptDeltaHasOutput(value)
	default:
		return false
	}
}

func textDeltaHasOutput(value *messages.TextDeltaValue) bool {
	return value != nil && value.Content != ""
}

func reasoningDeltaHasOutput(value *messages.ReasoningDeltaValue) bool {
	return value != nil && value.Content != ""
}

func audioDeltaHasOutput(value *messages.AudioDeltaValue) bool {
	return value != nil && len(value.Content) > 0
}

func imageDeltaHasOutput(value *messages.ImageDeltaValue) bool {
	return value != nil && len(value.Content) > 0
}

func videoDeltaHasOutput(value *messages.VideoDeltaValue) bool {
	return value != nil && len(value.Content) > 0
}

func fileDeltaHasOutput(value *messages.FileDeltaValue) bool {
	return value != nil && len(value.Content) > 0
}

func embeddingDeltaHasOutput(value *messages.EmbeddingDeltaValue) bool {
	return value != nil && len(value.Content) > 0
}

func toolCallDeltaHasOutput(value *messages.ToolCallDeltaValue) bool {
	return value != nil && value.PartialJSON != ""
}

func refusalHasOutput(value *messages.RefusalValue) bool {
	return value != nil && value.Message != ""
}

func transcriptDeltaHasOutput(value *messages.TranscriptDeltaValue) bool {
	return value != nil && value.Text != ""
}

func (r *reporter) observeSessionCloseLocked(msg messages.StreamMessage, leadingNewline bool) {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok || value == nil {
		return
	}
	candidate := &candidate{value: cloneSessionCloseValue(value), leadingNewline: leadingNewline}
	switch value.TerminalReason {
	case messages.TerminalReasonReplayComplete:
		r.outcome.replayComplete = true
		r.outcome.completion = completionComplete
	case messages.TerminalReasonCancellation:
		rememberCandidate(&r.outcome.cancellation, candidate)
	case maxDurationReason:
		rememberCandidate(&r.outcome.durationTerminal, candidate)
		r.outcome.durationExpired = true
	case messages.TerminalReasonTerminalFailure,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete:
		rememberCandidate(&r.outcome.failure, candidate)
	case messages.TerminalReasonProviderAuthoredCompletion,
		messages.TerminalReasonLoopSynthesizedCompletion,
		messages.TerminalReasonSessionClose,
		messages.TerminalReasonPartialOutput,
		messages.TerminalReasonProviderClose:
		rememberObservedCandidate(&r.outcome.observedTerminal, candidate)
	default:
		rememberObservedCandidate(&r.outcome.observedTerminal, candidate)
	}
}

func (r *reporter) observeErrorLocked(value *messages.ErrorValue, leadingNewline bool) {
	reason := boundReason(value.TerminalReason)
	if reason == "" {
		reason = messages.TerminalReasonTerminalFailure
	}
	classification := boundTerminalText(value.Classification)
	if classification == "" {
		classification = string(reason)
	}
	provenance := boundProvenance(value.TerminalProvenance)
	if provenance == "" {
		provenance = messages.TerminalProvenanceSession
	}
	outputState := boundOutputState(value.OutputState)
	if outputState == "" {
		outputState = r.outcome.outputState
	}
	if outputState == "" {
		outputState = messages.TerminalOutputNone
	}
	candidate := &candidate{
		value: messages.NewSessionCloseValueWithTerminal(
			"", "", classification, reason, provenance, outputState,
		),
		leadingNewline: leadingNewline,
	}
	if reason == messages.TerminalReasonCancellation {
		rememberCandidate(&r.outcome.cancellation, candidate)
		return
	}
	rememberCandidate(&r.outcome.failure, candidate)
}
