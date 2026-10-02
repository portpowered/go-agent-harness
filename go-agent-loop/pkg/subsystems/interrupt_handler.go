package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// ExecutionCanceller is implemented by participant runners that support mid-stream
// cancellation of the current request without stopping the runner goroutine.
type ExecutionCanceller interface {
	CancelCurrentExecution()
}

// InterruptHandler is a subsystem that watches for interrupt control-plane messages
// from the user and performs a clean mid-stream interrupt of the current execution:
//
//  1. Reconstruct and save any partial model response accumulated so far.
//  2. Cancel the in-flight model and tool executions via their per-execution contexts.
//  3. Optionally add the interrupt message's text content as a new user turn.
//  4. Increment CurrentPassID so any stale deltas still queued in the runners'
//     DeltaOutbox are silently dropped by the ordering layer.
//  5. Dispatch a fresh InferenceRequest with the updated conversation.
//
// History stays as it happened: a bare interrupt (no text, no held turns)
// dispatches a request that ends on the saved partial assistant message. The
// Inferencer turns that into a valid request; go-llm-gateway appends a
// "Continue." user turn, since Claude 4.6 and later reject a trailing
// assistant message as a prefill.
//
// InterruptHandler runs at TickGroupInterruptHandler (-1) so it executes before
// the Coordinator sees the current tick's inputs.
type InterruptHandler struct {
	modelCanceller ExecutionCanceller
	toolCanceller  ExecutionCanceller
	logger         logging.Logger
}

var _ Subsystem = (*InterruptHandler)(nil)

// NewInterruptHandler returns an InterruptHandler that cancels the given runners
// when an interrupt message is received.
func NewInterruptHandler(
	modelCanceller ExecutionCanceller,
	toolCanceller ExecutionCanceller,
	logger logging.Logger,
) *InterruptHandler {
	return &InterruptHandler{
		modelCanceller: modelCanceller,
		toolCanceller:  toolCanceller,
		logger:         logger,
	}
}

func (h *InterruptHandler) logInfo(msg string, fields ...logging.Field) {
	if h.logger != nil {
		h.logger.Info(msg, fields...)
	}
}

// TickGroup implements Subsystem.
func (h *InterruptHandler) TickGroup() TickGroup { return TickGroupInterruptHandler }

// Execute implements Subsystem. It checks UserControlPlaneMessage for an interrupt
// signal and, if found, performs the interrupt sequence described above.
func (h *InterruptHandler) Execute(ctx context.Context, curr *state.LoopState) error {
	if !hasInterruptMessage(curr.Inputs.UserControlPlaneMessage) {
		return nil
	}

	h.logInfo("interrupt_handler: interrupt received, cancelling current execution")

	// 1. Reconstruct and save any partial model response that has been streamed so
	//    far. We reconstruct from the committed delta window for the current response.
	//    UpdateWorldHistory has already run by the time Execute is called, so
	//    ConversationDeltaBuffer contains all deltas up to and including this tick.
	if curr.History.CurrentModelDeltaCount > 0 {
		start := curr.History.ModelDeltaStartIndex
		end := start + curr.History.CurrentModelDeltaCount
		if end <= len(curr.History.ConversationDeltaBuffer) {
			partialDeltas := curr.History.ConversationDeltaBuffer[start:end]
			partial := messages.ReconstructModelMessageFromDeltas(partialDeltas)
			if hasContent(partial) {
				h.logInfo("interrupt_handler: saving partial model response")
				curr.History.ConversationBuffer = append(curr.History.ConversationBuffer, partial)
			}
		}
	}

	// 2. Cancel in-flight executions. This causes the runners' per-execution
	//    contexts to be cancelled; the runner goroutines remain alive and will
	//    wait for the next request on their Inbox.
	if h.modelCanceller != nil {
		h.modelCanceller.CancelCurrentExecution()
	}
	if h.toolCanceller != nil {
		h.toolCanceller.CancelCurrentExecution()
	}

	// Every tool call the interrupt cut off gets a cancelled result, so no
	// tool call is left without its result, which providers reject.
	for _, result := range cancelledToolResults(curr.History.ConversationBuffer) {
		recordFullMessage(ctx, curr, messages.Tool, result)
		curr.History.ConversationBuffer = append(curr.History.ConversationBuffer, result)
	}

	// Held user turns were sent before the interrupt; they precede its text.
	PlaceHeldUserMessages(curr, func(message messages.Message) {
		recordFullMessage(ctx, curr, messages.User, message)
	})

	// 3. If the interrupt message carries text content, add it as a user turn so
	//    the resumed inference has the caller's follow-up instruction.
	for _, msg := range curr.Inputs.UserControlPlaneMessage {
		if isInterruptMessage(msg) {
			if text := msg.TextContent(); text != "" {
				curr.History.ConversationBuffer = append(
					curr.History.ConversationBuffer,
					messages.NewTextMessage(messages.RoleUser, text),
				)
			}
			break
		}
	}

	// 4. Increment CurrentPassID. Any deltas still queued in DeltaOutbox from the
	//    cancelled executions will have a lower LoopPassID and be dropped by the
	//    ordering layer.
	curr.History.CurrentPassID++

	// Reset model delta tracking so the next response starts a fresh window.
	curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
	curr.History.CurrentModelDeltaCount = 0

	// 5. Dispatch a new inference request with the updated conversation so the
	//    model answers with the (possibly partial) response in context.
	h.logInfo("interrupt_handler: dispatching resumed inference request",
		logging.Field{Key: "passID", Value: curr.History.CurrentPassID})
	curr.Outputs.ModelInbox.Write(ctx, messages.InferenceRequest{
		Messages:   curr.History.ConversationBuffer,
		Tools:      curr.Tools,
		LoopPassID: curr.History.CurrentPassID,
	})

	return nil
}

// hasInterruptMessage returns true if any message in the slice contains an
// interrupt ControlPlanePart.
func hasInterruptMessage(msgs []messages.Message) bool {
	for _, m := range msgs {
		if isInterruptMessage(m) {
			return true
		}
	}
	return false
}

// isInterruptMessage returns true if m contains a ControlPlanePart with type interrupt.
func isInterruptMessage(m messages.Message) bool {
	for _, p := range m.ContentParts {
		if cp, ok := p.(messages.ControlPlanePart); ok {
			if cp.ControlPlaneMessageType == messages.ControlPlaneMessageTypeInterrupt {
				return true
			}
		}
	}
	return false
}

// hasContent returns true if the message has any non-empty content (text, tool
// calls, or multimodal parts). Used to skip saving empty partial messages.
func hasContent(m messages.Message) bool {
	if len(m.ToolCalls) > 0 {
		return true
	}
	for _, p := range m.ContentParts {
		switch v := p.(type) {
		case messages.TextPart:
			if v.Text != "" {
				return true
			}
		case messages.ReasoningPart:
			if v.Reasoning != "" {
				return true
			}
		case messages.ImagePart:
			if len(v.Bytes) > 0 || v.URL != "" {
				return true
			}
		case messages.AudioPart:
			if len(v.Bytes) > 0 || v.URL != "" {
				return true
			}
		case messages.VideoPart:
			if len(v.Bytes) > 0 || v.URL != "" {
				return true
			}
		case messages.FilePart:
			if len(v.Bytes) > 0 || v.URL != "" {
				return true
			}
		}
	}
	return false
}

// interruptedToolResultText is the content of a synthesized result for a tool
// call that an interrupt cancelled before it returned.
const interruptedToolResultText = "cancelled: interrupted before the tool returned a result"

// cancelledToolResults returns a cancelled result for each tool call of the
// latest assistant tool-call message that has no result after it.
func cancelledToolResults(history []messages.Message) []messages.Message {
	callsAt := -1
	for index := len(history) - 1; index >= 0 && callsAt < 0; index-- {
		switch {
		case history[index].Role == messages.RoleAssistant && len(history[index].ToolCalls) > 0:
			callsAt = index
		case history[index].Role == messages.RoleUser:
			return nil
		}
	}
	if callsAt < 0 {
		return nil
	}
	answered := make(map[string]bool)
	for _, message := range history[callsAt+1:] {
		if message.Role == messages.RoleTool {
			answered[message.ToolCallID] = true
		}
	}
	var results []messages.Message
	for _, call := range history[callsAt].ToolCalls {
		if !answered[call.ID] {
			results = append(results, messages.Message{
				Role:         messages.RoleTool,
				ToolCallID:   call.ID,
				Name:         call.Name,
				ContentParts: []messages.ContentPart{messages.NewTextPart(interruptedToolResultText)},
			})
		}
	}
	return results
}

// recordFullMessage records a message the interrupt adds to history on the
// kernel's full-message stream, as the coordinator does for the messages it
// adds, so the stream and history agree.
func recordFullMessage(ctx context.Context, curr *state.LoopState, source messages.ParticipantID, message messages.Message) {
	if curr.Outputs.KernelDeltaInbox == nil {
		return
	}
	messages.WriteKernelDelta(ctx, curr.Outputs.KernelDeltaInbox, messages.KernelDeltaRequest{
		Source: source,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue(string(source), message),
		},
	})
}
