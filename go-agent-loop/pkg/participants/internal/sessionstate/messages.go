package sessionstate

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ResponseID normalizes a provider response identity.
func ResponseID(value string) string {
	return strings.TrimSpace(value)
}

// IsContinuationCreate reports whether evt requests a tool continuation.
func IsContinuationCreate(evt messages.StreamMessage) bool {
	value, ok := evt.Value.(*messages.ResponseCreateValue)
	return evt.Type == messages.StreamTypeResponseCreate && ok && value.IsToolContinuation()
}

// IsAcknowledgementCreate reports whether evt requests a progress acknowledgement.
func IsAcknowledgementCreate(evt messages.StreamMessage) bool {
	if evt.Type != messages.StreamTypeResponseCreate {
		return false
	}
	value, ok := evt.Value.(*messages.ResponseCreateValue)
	return ok && value.IsToolAcknowledgement()
}

// IsResponseStreamType reports whether typ is scoped to a provider response.
func IsResponseStreamType(typ messages.StreamMessageType) bool {
	switch typ { //nolint:exhaustive // Response-scoped stream types; session and control types are not.
	case messages.StreamTypeMessageStart,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart,
		messages.StreamTypeImageDelta,
		messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart,
		messages.StreamTypeFileDelta,
		messages.StreamTypeFileEnd,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeRefusal,
		messages.StreamTypeUsageInfo:
		return true
	default:
		return false
	}
}

func isCustomerOutputDelta(msg messages.StreamMessage) bool {
	// A user transcript is session input, not stale assistant output. In
	// particular, preserving it after a barge-in keeps the recognized words in
	// the recording even while late assistant output is filtered.
	if msg.Role == messages.RoleUser && (msg.Type == messages.StreamTypeTranscriptDelta || msg.Type == messages.StreamTypeTranscriptEnd) {
		return false
	}
	switch msg.Type { //nolint:exhaustive // Classifies customer-visible deltas; every other type stays visible after a cancel.
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

func interruptedMessageEndValue(value messages.StreamMessageValue, hasOutput bool) messages.StreamMessageValue {
	end, ok := value.(*messages.MessageEndValue)
	if !ok || end == nil {
		return value
	}
	outputState := messages.TerminalOutputNone
	if hasOutput {
		outputState = messages.TerminalOutputPartial
	}
	return messages.NewMessageEndValueWithTerminal(end.Usage, messages.TerminalReasonPartialOutput, messages.TerminalProvenanceLoop, outputState)
}
