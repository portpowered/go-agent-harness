package engine

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-loop/pkg/state"
)

// hasControlPlanePart returns true if m contains at least one ControlPlanePart,
// indicating the message should be routed to UserControlPlaneMessage rather than
// UserOutputMessage so the Coordinator does not treat it as a regular user turn.
func hasControlPlanePart(m messages.Message) bool {
	for _, p := range m.ContentParts {
		if _, ok := p.(messages.ControlPlanePart); ok {
			return true
		}
	}
	return false
}

// GlobalOrdering maintains global message ordering and owns all input consumption for the
// agent loop. It assigns a strictly increasing global index to each consumed message or
// delta (see ORDERING.md). The engine passes loop state into ReadTick, UpdateWorldHistory,
// and FlushInputs each tick.
//
// Message assembly: messages are reconstructed from the history of deltas (ConversationDeltaBuffer
// plus current tick's ModelInputDelta/ToolInputDelta) rather than from in-memory assembly state.
// On REASONING.END or MESSAGE.END the ordering reconstructs from the relevant delta slice and
// emits the assembled Message. Partial or interrupted streams can be reconstructed from deltas
// up to the last received delta.
type GlobalOrdering struct {
	modelRunner       *participants.ModelRunner
	toolRunner        *participants.ToolRunner
	userRunner        *participants.UserRunner
	interactionRunner *participants.InteractionRunner
	logger            logging.Logger
}

// NewGlobalOrdering returns an ordering that consumes from the given runners. toolRunner
// and userRunner may be nil.
func NewGlobalOrdering(
	modelRunner *participants.ModelRunner,
	toolRunner *participants.ToolRunner,
	userRunner *participants.UserRunner,
	logger logging.Logger,
) *GlobalOrdering {
	return &GlobalOrdering{
		modelRunner: modelRunner,
		toolRunner:  toolRunner,
		userRunner:  userRunner,
		logger:      logger,
	}
}

func (o *GlobalOrdering) SetInteractionRunner(r *participants.InteractionRunner) {
	o.interactionRunner = r
}

func (o *GlobalOrdering) assignStreamOrdering(ts *state.LoopState, d messages.StreamMessage, actor messages.ParticipantID) messages.StreamMessage {
	d.GlobalIndex = ts.History.NextGlobalIndex
	d.ActorID = actor
	ts.History.NextGlobalIndex++
	return d
}

func (o *GlobalOrdering) assignMessageOrdering(ts *state.LoopState, m messages.Message, actor messages.ParticipantID) messages.Message {
	m.GlobalIndex = ts.History.NextGlobalIndex
	m.ActorID = actor
	ts.History.NextGlobalIndex++
	return m
}

// ReadTick consumes one item from the participant outboxes (deltas preferred, then
// full messages), assigns global ordering, and appends to ts.Inputs. When a delta
// completes a message (MESSAGE.END) the assembled Message is reconstructed from
// delta history and appended to the appropriate Inputs slice. Returns an error on
// context cancellation or when a runner signals failure via an ERROR delta.
func (o *GlobalOrdering) ReadTick(ctx context.Context, ts *state.LoopState) error {
	modelDeltaOut := o.modelRunner.DeltaOutbox.Chan()

	var toolDeltaOut <-chan messages.StreamMessage
	if o.toolRunner != nil {
		toolDeltaOut = o.toolRunner.DeltaOutbox.Chan()
	}

	var userOut <-chan messages.UserResponse
	if o.userRunner != nil {
		userOut = o.userRunner.Outbox.Chan()
	}

	var interactionOut <-chan messages.InteractionEvent
	if o.interactionRunner != nil {
		interactionOut = o.interactionRunner.Outbox.Chan()
	}

	// Prefer deltas over full messages for ordering.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case delta := <-modelDeltaOut:
		if o.logger != nil {
			o.logger.Debug("engine: model delta", logging.Field{Key: "delta", Value: delta})
		}
		return o.consumeModelDelta(ts, delta)
	case delta := <-toolDeltaOut:
		return o.consumeToolDelta(ts, delta)
	case event := <-interactionOut:
		ts.Inputs.InteractionEvents = append(ts.Inputs.InteractionEvents, event)
		return nil
	default:
	}

	// Then full messages.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case delta := <-modelDeltaOut:
		if o.logger != nil {
			o.logger.Debug("engine: model delta", logging.Field{Key: "delta", Value: delta})
		}
		return o.consumeModelDelta(ts, delta)
	case delta := <-toolDeltaOut:
		return o.consumeToolDelta(ts, delta)
	case event := <-interactionOut:
		ts.Inputs.InteractionEvents = append(ts.Inputs.InteractionEvents, event)
		return nil
	case userResp := <-userOut:
		if userResp.Error != nil {
			return userResp.Error
		}
		msg := o.assignMessageOrdering(ts, userResp.Message, messages.User)
		if hasControlPlanePart(msg) {
			ts.Inputs.UserControlPlaneMessage = append(ts.Inputs.UserControlPlaneMessage, msg)
		} else {
			ts.Inputs.UserOutputMessage = append(ts.Inputs.UserOutputMessage, msg)
		}
	}

	return nil
}

// modelDeltasForCurrentResponse returns the slice of model deltas for the response
// currently being streamed: history from ModelDeltaStartIndex plus current tick's
// ModelInputDelta (including the delta just appended).
func (o *GlobalOrdering) modelDeltasForCurrentResponse(ts *state.LoopState) []messages.StreamMessage {
	buf := ts.History.ConversationDeltaBuffer
	start := ts.History.ModelDeltaStartIndex
	count := ts.History.CurrentModelDeltaCount
	var out []messages.StreamMessage
	if start+count <= len(buf) {
		out = make([]messages.StreamMessage, 0, count+len(ts.Inputs.ModelInputDelta))
		out = append(out, buf[start:start+count]...)
	}
	out = append(out, ts.Inputs.ModelInputDelta...)
	return out
}

// toolDeltasForCurrentBatch returns the slice of tool deltas for the batch currently
// being streamed: history from ToolDeltaStartIndex plus current tick's ToolInputDelta
// (including the delta just appended).
func (o *GlobalOrdering) toolDeltasForCurrentBatch(ts *state.LoopState) []messages.StreamMessage {
	buf := ts.History.ConversationDeltaBuffer
	start := ts.History.ToolDeltaStartIndex
	count := ts.History.CurrentToolDeltaCount
	var out []messages.StreamMessage
	if start+count <= len(buf) {
		out = make([]messages.StreamMessage, 0, count+len(ts.Inputs.ToolInputDelta))
		out = append(out, buf[start:start+count]...)
	}
	out = append(out, ts.Inputs.ToolInputDelta...)
	return out
}

// consumeModelDelta processes one delta from the model runner's DeltaOutbox.
// ERROR deltas clean up history and return an error. Stale deltas (LoopPassID
// lower than current) are silently dropped to handle mid-stream interrupts.
// All other deltas are recorded in ModelInputDelta. On REASONING.END or
// MESSAGE.END the message is reconstructed from delta history and emitted into
// ModelOutputMessage.
func (o *GlobalOrdering) consumeModelDelta(ts *state.LoopState, delta messages.StreamMessage) error {
	// Drop deltas that belong to a superseded pass (arrived after an interrupt).
	if delta.LoopPassID > 0 && delta.LoopPassID < ts.History.CurrentPassID {
		return nil
	}

	if ev, isErr := delta.Value.(*messages.ErrorValue); isErr {
		// Roll back any model deltas recorded so far in this response.
		start := ts.History.ModelDeltaStartIndex
		if start < len(ts.History.ConversationDeltaBuffer) {
			ts.History.ConversationDeltaBuffer = ts.History.ConversationDeltaBuffer[:start]
		}
		ts.History.CurrentModelDeltaCount = 0
		// Drain any remaining buffered deltas so they do not pollute the next response.
		for {
			if _, ok := o.modelRunner.DeltaOutbox.Read(); !ok {
				break
			}
		}
		return errors.New(ev.Message)
	}

	assigned := o.assignStreamOrdering(ts, delta, messages.Model)
	ts.Inputs.ModelInputDelta = append(ts.Inputs.ModelInputDelta, assigned)

	switch assigned.Value.(type) {
	case *messages.ReasoningEndValue:
		deltas := o.modelDeltasForCurrentResponse(ts)
		msg := ReconstructModelMessageFromDeltas(deltas)
		// Emit only reasoning part(s), matching previous behavior of separate reasoning message.
		var reasoningParts []messages.ContentPart
		for _, p := range msg.ContentParts {
			if _, ok := p.(messages.ReasoningPart); ok {
				reasoningParts = append(reasoningParts, p)
			}
		}
		if len(reasoningParts) > 0 {
			ts.Inputs.ModelOutputMessage = append(ts.Inputs.ModelOutputMessage, o.assignMessageOrdering(ts, messages.Message{
				Role:         messages.RoleAssistant,
				ContentParts: reasoningParts,
			}, messages.Model))
		}
	case *messages.MessageEndValue:
		deltas := o.modelDeltasForCurrentResponse(ts)
		msg := ReconstructModelMessageFromDeltas(deltas)
		// Emit full message but strip reasoning (already emitted at REASONING.END).
		var parts []messages.ContentPart
		for _, p := range msg.ContentParts {
			if _, ok := p.(messages.ReasoningPart); !ok {
				parts = append(parts, p)
			}
		}
		msg.ContentParts = parts
		ts.Inputs.ModelOutputMessage = append(ts.Inputs.ModelOutputMessage, o.assignMessageOrdering(ts, msg, messages.Model))
	}

	return nil
}

// consumeToolDelta processes one delta from the tool runner's DeltaOutbox.
// ERROR deltas return an error. Stale deltas (LoopPassID lower than current)
// are silently dropped. MESSAGE.START resets the tool batch tracking so
// reconstruction knows where this batch's deltas begin in ConversationDeltaBuffer.
// MESSAGE.END triggers reconstruction from the full delta history for this batch
// and emits one Message per tool into ToolOutputMessage.
func (o *GlobalOrdering) consumeToolDelta(ts *state.LoopState, delta messages.StreamMessage) error {
	// Drop deltas that belong to a superseded pass (arrived after an interrupt).
	if delta.LoopPassID > 0 && delta.LoopPassID < ts.History.CurrentPassID {
		return nil
	}

	if ev, isErr := delta.Value.(*messages.ErrorValue); isErr {
		return errors.New(ev.Message)
	}

	assigned := o.assignStreamOrdering(ts, delta, messages.Tool)
	ts.Inputs.ToolInputDelta = append(ts.Inputs.ToolInputDelta, assigned)

	switch assigned.Value.(type) {
	case *messages.MessageStartValue:
		// Mark where this batch's deltas will begin in ConversationDeltaBuffer.
		// UpdateWorldHistory hasn't run yet so len(buf) is the insertion point.
		ts.History.ToolDeltaStartIndex = len(ts.History.ConversationDeltaBuffer)
		ts.History.CurrentToolDeltaCount = 0
	case *messages.MessageEndValue:
		deltas := o.toolDeltasForCurrentBatch(ts)
		msgs := ReconstructToolMessagesFromDeltas(deltas)
		for _, msg := range msgs {
			ts.Inputs.ToolOutputMessage = append(ts.Inputs.ToolOutputMessage, o.assignMessageOrdering(ts, msg, messages.Tool))
		}
	}

	return nil
}

// ReconstructModelMessageFromDeltas builds a single assistant Message from a slice
// of model stream deltas. Works for full messages (including MESSAGE.END) and for
// partial/interrupted streams (e.g. only TEXT.DELTA so far).
// The implementation lives in the messages package to allow other packages (e.g. subsystems)
// to call it without creating an import cycle.
func ReconstructModelMessageFromDeltas(deltas []messages.StreamMessage) messages.Message {
	return messages.ReconstructModelMessageFromDeltas(deltas)
}

// ReconstructToolMessagesFromDeltas builds one Message per tool result from a
// slice of tool stream deltas. Expects MESSAGE.START to begin a batch and
// MESSAGE.END to end it; ToolCallId on each delta associates content with a tool.
// Works for full batches and for partial/interrupted (partial content per tool).
// The implementation lives in the messages package to allow other packages (e.g. subsystems)
// to call it without creating an import cycle.
func ReconstructToolMessagesFromDeltas(deltas []messages.StreamMessage) []messages.Message {
	return messages.ReconstructToolMessagesFromDeltas(deltas)
}

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
	// Truncation (removing stale entries past the current response boundary) is only
	// applied when model deltas are actually written this tick — otherwise it would
	// incorrectly cut off tool/user deltas that were appended in earlier ticks.
	for i, msg := range ts.Inputs.ModelInputDelta {
		idx := ts.History.ModelDeltaStartIndex + ts.History.CurrentModelDeltaCount + i
		if idx < len(ts.History.ConversationDeltaBuffer) {
			ts.History.ConversationDeltaBuffer[idx] = msg
		} else {
			ts.History.ConversationDeltaBuffer = append(ts.History.ConversationDeltaBuffer, msg)
		}
	}
	if len(ts.Inputs.ModelInputDelta) > 0 {
		ts.History.CurrentModelDeltaCount += len(ts.Inputs.ModelInputDelta)
		newLen := ts.History.ModelDeltaStartIndex + ts.History.CurrentModelDeltaCount
		if newLen < len(ts.History.ConversationDeltaBuffer) {
			ts.History.ConversationDeltaBuffer = ts.History.ConversationDeltaBuffer[:newLen]
		}
	}

	// Tool and user deltas are appended after model delta logic so they are never
	// affected by the model delta truncation.
	ts.History.ConversationDeltaBuffer = append(ts.History.ConversationDeltaBuffer, ts.Inputs.ToolInputDelta...)
	ts.History.CurrentToolDeltaCount += len(ts.Inputs.ToolInputDelta)

	ts.History.ConversationDeltaBuffer = append(ts.History.ConversationDeltaBuffer, ts.Inputs.UserInputDelta...)
}

// FlushInputs clears all input slices and resets TerminateLoop for the next tick.
func (o *GlobalOrdering) FlushInputs(ts *state.LoopState) {
	ts.Inputs.ToolControlPlaneMessage = ts.Inputs.ToolControlPlaneMessage[:0]
	ts.Inputs.ToolOutputMessage = ts.Inputs.ToolOutputMessage[:0]
	ts.Inputs.ToolInputDelta = ts.Inputs.ToolInputDelta[:0]
	ts.Inputs.UserControlPlaneMessage = ts.Inputs.UserControlPlaneMessage[:0]
	ts.Inputs.UserOutputMessage = ts.Inputs.UserOutputMessage[:0]
	ts.Inputs.UserInputDelta = ts.Inputs.UserInputDelta[:0]
	ts.Inputs.ModelControlPlaneMessage = ts.Inputs.ModelControlPlaneMessage[:0]
	ts.Inputs.ModelOutputMessage = ts.Inputs.ModelOutputMessage[:0]
	ts.Inputs.ModelInputDelta = ts.Inputs.ModelInputDelta[:0]
	ts.Inputs.InteractionEvents = ts.Inputs.InteractionEvents[:0]
	ts.Inputs.TerminateLoop = false
}
