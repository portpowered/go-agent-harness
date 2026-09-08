package engine

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// UpdateWorldHistory moves the current tick's inputs (ToolOutputMessage, UserOutputMessage,
// ModelOutputMessage, ToolInputDelta, UserInputDelta, ModelInputDelta) into History
// (ConversationBuffer and ConversationDeltaBuffer).
//
// Model deltas are written/truncated first so that the model-delta truncation logic
// (which ensures no stale deltas linger past the current response end) cannot cut off
// tool or user deltas that are appended in the same tick.
func (o *GlobalOrdering) UpdateWorldHistory(ts *state.LoopState) {
	ts.History.ConversationBuffer = append(ts.History.ConversationBuffer, ts.Inputs.ToolOutputMessage...)
	ts.History.ConversationBuffer = append(ts.History.ConversationBuffer, ts.Inputs.UserOutputMessage...)
	ts.History.ConversationBuffer = append(ts.History.ConversationBuffer, ts.Inputs.ModelOutputMessage...)

	// Model deltas: written at ModelDeltaStartIndex+CurrentModelDeltaCount.
	// If a tool stream is already in the shared buffer, insert the model delta
	// before it instead of overwriting it. The model window remains contiguous
	// for reconstruction while the tool batch retains every delta it needs.
	// Truncation (removing stale entries past the current response boundary) is only
	// applied when model deltas are actually written this tick — otherwise it would
	// incorrectly cut off tool/user deltas that were appended in earlier ticks.
	writtenModelDeltaCount := 0
	for _, msg := range ts.Inputs.ModelInputDelta {
		if msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
			// Keep acknowledgement deltas on the kernel-facing input path, but
			// do not persist them in the conversation delta history. They are
			// progress output for an in-flight tool, not model context.
			continue
		}
		idx := ts.History.ModelDeltaStartIndex + ts.History.CurrentModelDeltaCount + writtenModelDeltaCount
		if idx < len(ts.History.ConversationDeltaBuffer) && isModelHistoryDelta(ts.History.ConversationDeltaBuffer[idx]) {
			ts.History.ConversationDeltaBuffer[idx] = msg
		} else if idx < len(ts.History.ConversationDeltaBuffer) {
			ts.History.ConversationDeltaBuffer = insertHistoryDelta(ts.History.ConversationDeltaBuffer, idx, msg)
			o.shiftToolDeltaStart(ts, idx)
		} else {
			ts.History.ConversationDeltaBuffer = append(ts.History.ConversationDeltaBuffer, msg)
		}
		writtenModelDeltaCount++
	}
	if writtenModelDeltaCount > 0 {
		ts.History.CurrentModelDeltaCount += writtenModelDeltaCount
		o.dropStaleModelDeltas(ts)
	}

	// Tool and user deltas are appended after model delta logic so they are never
	// affected by the model delta truncation.
	ts.History.ConversationDeltaBuffer = append(ts.History.ConversationDeltaBuffer, ts.Inputs.ToolInputDelta...)
	ts.History.CurrentToolDeltaCount += len(ts.Inputs.ToolInputDelta)

	ts.History.ConversationDeltaBuffer = append(ts.History.ConversationDeltaBuffer, ts.Inputs.UserInputDelta...)
}

func isModelHistoryDelta(delta messages.StreamMessage) bool {
	return delta.ActorID == messages.Model || (delta.ActorID == "" && delta.Role == messages.RoleAssistant)
}

func insertHistoryDelta(history []messages.StreamMessage, index int, delta messages.StreamMessage) []messages.StreamMessage {
	history = append(history, messages.StreamMessage{})
	copy(history[index+1:], history[index:])
	history[index] = delta
	return history
}

func (o *GlobalOrdering) shiftToolDeltaStart(ts *state.LoopState, insertedAt int) {
	if (o.toolBatchActive || ts.History.CurrentToolDeltaCount > 0) && insertedAt <= ts.History.ToolDeltaStartIndex {
		ts.History.ToolDeltaStartIndex++
	}
}

func (o *GlobalOrdering) dropStaleModelDeltas(ts *state.LoopState) {
	start := ts.History.ModelDeltaStartIndex
	end := start + ts.History.CurrentModelDeltaCount
	if end >= len(ts.History.ConversationDeltaBuffer) {
		return
	}
	toolStart := ts.History.ToolDeltaStartIndex
	write := end
	removedBeforeTool := 0
	for read := end; read < len(ts.History.ConversationDeltaBuffer); read++ {
		delta := ts.History.ConversationDeltaBuffer[read]
		if isModelHistoryDelta(delta) {
			if read < toolStart {
				removedBeforeTool++
			}
			continue
		}
		ts.History.ConversationDeltaBuffer[write] = delta
		write++
	}
	ts.History.ConversationDeltaBuffer = ts.History.ConversationDeltaBuffer[:write]
	if removedBeforeTool > 0 {
		ts.History.ToolDeltaStartIndex -= removedBeforeTool
	}
}
