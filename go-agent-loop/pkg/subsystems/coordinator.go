package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// The coordinator is responsible for executing at each tick, checking the inputs, and then determining to push the next inference step/tool call based on the world state.
// All full messages (model, tool, user) are routed through kernelDeltaInbox as
// SYSTEM.FULL_MESSAGE stream messages so that they share the same FIFO queue
// as streaming deltas, eliminating ordering races between the two paths.

type Coordinator struct {
	logger logging.Logger
}

func NewCoordinator(
	logger logging.Logger) *Coordinator {
	return &Coordinator{
		logger: logger,
	}
}

var _ Subsystem = (*Coordinator)(nil)

func (c *Coordinator) logInfo(msg string, fields ...logging.Field) {
	if c.logger != nil {
		c.logger.Info(msg, fields...)
	}
}

// sendInferenceResult pushes a full message to the kernel via the shared delta
// inbox as a SYSTEM.FULL_MESSAGE event.
func (c *Coordinator) sendInferenceResult(ctx context.Context, state *state.LoopState, source messages.ParticipantID, msg messages.Message) {
	state.Outputs.KernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: source,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue(string(source), msg),
		},
	})
}

// Execute implements [Subsystem].
// user -> triggers agent
// agent -> triggers tool call,
// tool output -> triggers agent
// agent -(if has no tool call)-> user (close current loop on current turn end)
func (c *Coordinator) Execute(ctx context.Context, curr *state.LoopState) error {
	if len(curr.Inputs.ToolOutputMessage) > 0 {
		c.logInfo("Coordinator: tool text output message", logging.Field{Key: "curr.Inputs.ToolOutputMessage", Value: curr.Inputs.ToolOutputMessage})
		// Dispatch tool messages to kernel via unified delta inbox.
		for _, message := range curr.Inputs.ToolOutputMessage {
			c.sendInferenceResult(ctx, curr, messages.Tool, message)
		}
		// if the input receives a message from the tool gateway, then trigger a new assistant message from that call.
		curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
		curr.History.CurrentModelDeltaCount = 0
		curr.History.CurrentPassID++
		// The kernel records full messages asynchronously through the shared
		// delta inbox. Include this completed tool batch in the request snapshot
		// as well, so a session model runner can deliver rich results to the
		// provider before the kernel's history tick catches up.
		conversation := append([]messages.Message(nil), curr.History.ConversationBuffer...)
		if !toolResultsAtHistoryTail(conversation, curr.Inputs.ToolOutputMessage) {
			conversation = append(conversation, curr.Inputs.ToolOutputMessage...)
		}
		curr.Outputs.ModelInbox.Write(ctx, messages.NewInferenceRequest(
			conversation, curr.Tools, curr.History.CurrentPassID, curr.InferenceDefaults,
		))
		return nil
	}

	if len(curr.Inputs.ModelOutputMessage) > 0 {
		// Dispatch model messages to kernel via unified delta inbox. Ordering is
		// guaranteed because SYSTEM.FULL_MESSAGE and streaming deltas share the
		// same FIFO queue. CoordinatorDelta (which runs after Coordinator) sends
		// LOOP.END through that same queue, so the kernel always processes all
		// messages before the stream closes.
		for _, message := range curr.Inputs.ModelOutputMessage {
			c.sendInferenceResult(ctx, curr, messages.Model, message)
		}
		// Decide whether to trigger a tool call or deliver to the user.
		// Reasoning-only messages are recorded above but do not trigger further actions.
		hasFinalResponse := false
		for _, message := range curr.Inputs.ModelOutputMessage {
			switch {
			case len(message.ToolCalls) > 0 && !curr.ToolExecutionAvailable:
				// No tool executor is configured for this loop, so a
				// provider-issued tool call cannot be executed. Deliver the
				// message like a final response instead of dispatching a
				// batch into the idle default executor, whose guaranteed
				// failure surfaces as a racy terminal error after the
				// response already completed.
				c.logInfo("Coordinator: model tool call without a configured executor delivered as final response",
					logging.Field{Key: "tool_calls", Value: len(message.ToolCalls)})
				curr.Outputs.UserInbox.Write(ctx, messages.UserRequest{Message: message})
				hasFinalResponse = true
			case len(message.ToolCalls) > 0:
				c.logInfo("Coordinator: model tool call output message", logging.Field{Key: "message", Value: message})
				curr.History.CurrentPassID++
				curr.Outputs.ToolInbox.Write(ctx, messages.ToolBatchRequest{
					Calls:      message.ToolCalls,
					LoopPassID: curr.History.CurrentPassID,
				})
			case !message.HasOnlyReasoning():
				c.logInfo("Coordinator: model output message", logging.Field{Key: "message", Value: message})
				curr.Outputs.UserInbox.Write(ctx, messages.UserRequest{
					Message: message,
				})
				hasFinalResponse = true
			default:
				c.logInfo("Coordinator: model reasoning output message", logging.Field{Key: "message", Value: message})
			}
		}
		// Set TerminateLoop so CoordinatorDelta sends LOOP.END through the same
		// delta inbox. Because CoordinatorDelta runs after Coordinator (higher tick
		// group), LOOP.END is always enqueued after all SYSTEM.FULL_MESSAGE
		// messages, preserving ordering guarantees.
		//
		// In DuplexSession, auto-termination on final response is suppressed.
		// The session persists until explicitly closed via control plane
		// (session_close or stop).
		if hasFinalResponse && curr.Mode != state.DuplexSession {
			c.logInfo("Coordinator: terminating loop", logging.Field{Key: "hasFinalResponse", Value: hasFinalResponse})
			curr.Inputs.TerminateLoop = true
		}
		// The model delta window belongs to one provider response. Reset it after
		// dispatching the completed response so a later response (for example an
		// acknowledgement or an interruption response) cannot reconstruct and
		// execute tool calls from this response a second time.
		curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
		curr.History.CurrentModelDeltaCount = 0
		return nil
	}

	// In DuplexSession, check for session_close or stop control plane messages
	// from the user. These trigger graceful loop termination.
	if curr.Mode == state.DuplexSession {
		for _, msg := range curr.Inputs.UserControlPlaneMessage {
			if cpType := extractControlPlaneType(msg); cpType == messages.ControlPlaneMessageTypeSessionClose ||
				cpType == messages.ControlPlaneMessageTypeStop {
				c.logInfo("Coordinator: session close requested via control plane",
					logging.Field{Key: "type", Value: string(cpType)})
				curr.Inputs.TerminateLoop = true
				return nil
			}
		}
	}

	if len(curr.Inputs.UserOutputMessage) > 0 {
		// Dispatch user messages to kernel via unified delta inbox.
		for _, message := range curr.Inputs.UserOutputMessage {
			c.logInfo("Coordinator: user text output message", logging.Field{Key: "message", Value: message})
			c.sendInferenceResult(ctx, curr, messages.User, message)
		}
		curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
		curr.History.CurrentModelDeltaCount = 0
		curr.History.CurrentPassID++
		curr.Outputs.ModelInbox.Write(ctx, messages.NewInferenceRequest(
			curr.History.ConversationBuffer, curr.Tools, curr.History.CurrentPassID, curr.InferenceDefaults,
		))
	}
	return nil
}

// toolResultsAtHistoryTail handles the race between the coordinator's
// inference-request tick and the kernel tick that records SYSTEM.FULL_MESSAGE.
// A request must contain the current tool batch exactly once whether the
// kernel has already appended it or not.
func toolResultsAtHistoryTail(history, results []messages.Message) bool {
	if len(results) == 0 || len(history) < len(results) {
		return false
	}
	tail := history[len(history)-len(results):]
	for index := range results {
		if tail[index].Role != results[index].Role || tail[index].ToolCallID != results[index].ToolCallID {
			return false
		}
	}
	return true
}

// TickGroup implements [Subsystem].
func (c *Coordinator) TickGroup() TickGroup {
	return TickGroupCoordinator
}

// extractControlPlaneType returns the ControlPlaneMessageType from the first
// ControlPlanePart found in the message's ContentParts, or "" if none.
func extractControlPlaneType(msg messages.Message) messages.ControlPlaneMessageType {
	for _, part := range msg.ContentParts {
		if cp, ok := part.(messages.ControlPlanePart); ok {
			return cp.ControlPlaneMessageType
		}
	}
	return ""
}
