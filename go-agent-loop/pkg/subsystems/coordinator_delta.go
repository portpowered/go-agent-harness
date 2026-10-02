package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// CoordinatorDelta consumes streaming delta messages from the current tick inputs
// and dispatches them to the kernel participant's delta inbox for IO delivery.
type CoordinatorDelta struct {
	kernelDeltaInbox *messages.TypedBuffer[messages.KernelDeltaRequest]
	logger           logging.Logger
}

func NewCoordinatorDelta(kernelDeltaInbox *messages.TypedBuffer[messages.KernelDeltaRequest], logger logging.Logger) *CoordinatorDelta {
	return &CoordinatorDelta{
		kernelDeltaInbox: kernelDeltaInbox,
		logger:           logger,
	}
}

var _ Subsystem = (*CoordinatorDelta)(nil)

// Execute implements [Subsystem].
func (c *CoordinatorDelta) Execute(ctx context.Context, curr *state.LoopState) error {
	for _, delta := range curr.Inputs.ModelInputDelta {
		c.logInfo("CoordinatorDelta: model delta", logging.Field{Key: "delta", Value: delta})
		messages.WriteKernelDelta(ctx, c.kernelDeltaInbox, messages.KernelDeltaRequest{
			Source: messages.Model,
			Delta:  delta,
		})
	}

	for _, delta := range curr.Inputs.ToolInputDelta {
		c.logInfo("CoordinatorDelta: tool delta", logging.Field{Key: "delta", Value: delta})
		messages.WriteKernelDelta(ctx, c.kernelDeltaInbox, messages.KernelDeltaRequest{
			Source: messages.Tool,
			Delta:  delta,
		})
	}

	for _, delta := range curr.Inputs.UserInputDelta {
		c.logInfo("CoordinatorDelta: user delta", logging.Field{Key: "delta", Value: delta})
		messages.WriteKernelDelta(ctx, c.kernelDeltaInbox, messages.KernelDeltaRequest{
			Source: messages.User,
			Delta:  delta,
		})
	}

	if curr.Inputs.TerminateLoop {
		c.logInfo("CoordinatorDelta: terminating loop", logging.Field{Key: "terminateLoop", Value: curr.Inputs.TerminateLoop})
		// A subsystem after the coordinator (interaction events) may have
		// ended the loop: held user turns still precede LOOP.END.
		PlaceHeldUserMessages(curr, func(message messages.Message) {
			writeUserFullMessage(ctx, c.kernelDeltaInbox, message)
		})

		// In DuplexSession, emit SESSION.CLOSE before LOOP.END so consumers
		// see the session lifecycle bracket: SESSION.OPEN ... SESSION.CLOSE → LOOP.END.
		if curr.Mode == state.DuplexSession {
			reason := "client_close"
			// Derive reason from control plane messages if available.
			for _, msg := range curr.Inputs.UserControlPlaneMessage {
				if cpType := extractSessionCloseReason(msg); cpType != "" {
					reason = cpType
					break
				}
			}
			messages.WriteKernelDelta(ctx, c.kernelDeltaInbox, messages.KernelDeltaRequest{
				Source: messages.System,
				Delta: messages.StreamMessage{
					Type:  messages.StreamTypeSessionClose,
					Value: newLoopSessionCloseValue(curr.SessionID, reason),
				},
			})
		}

		messages.WriteKernelDelta(ctx, c.kernelDeltaInbox, messages.KernelDeltaRequest{
			Source: messages.System,
			Delta: messages.StreamMessage{
				Type:  messages.StreamTypeLoopEnd,
				Value: messages.NewLoopEndValue(),
			},
		})
	}
	return nil
}

func (c *CoordinatorDelta) logInfo(msg string, fields ...logging.Field) {
	if c.logger != nil {
		c.logger.Debug(msg, fields...)
	}
}

// TickGroup implements [Subsystem].
func (c *CoordinatorDelta) TickGroup() TickGroup {
	return TickGroupCoordinatorDelta
}

// sessionCloseReasonStop is the close reason for an explicit stop request.
const sessionCloseReasonStop = "stop"

// extractSessionCloseReason returns a reason string from a control plane message.
func extractSessionCloseReason(msg messages.Message) string {
	for _, part := range msg.ContentParts {
		if cp, ok := part.(messages.ControlPlanePart); ok {
			switch cp.ControlPlaneMessageType {
			case messages.ControlPlaneMessageTypeSessionClose:
				return "client_close"
			case messages.ControlPlaneMessageTypeStop:
				return sessionCloseReasonStop
			case messages.ControlPlaneMessageTypePause, messages.ControlPlaneMessageTypeResume, messages.ControlPlaneMessageTypeInterrupt, messages.ControlPlaneMessageTypePing:
				// Not session-close controls; keep scanning.
			}
		}
	}
	return ""
}

func newLoopSessionCloseValue(sessionID, reason string) *messages.SessionCloseValue {
	terminalReason := messages.TerminalReasonSessionClose
	if reason == sessionCloseReasonStop {
		terminalReason = messages.TerminalReasonCancellation
	}
	return messages.NewSessionCloseValueWithTerminal(
		sessionID,
		reason,
		string(terminalReason),
		terminalReason,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputNotApplicable,
	)
}

// trackOpenExchange follows the model response and tool batch lifecycles
// across this tick's deltas: a response is open from its first delta until
// its MESSAGE.END or a terminal ERROR, and a dispatched tool batch is
// outstanding until its MESSAGE.END. Stale-pass deltas never reach the
// inputs, and tool acknowledgements are not responses of their own. An
// interrupt cancels both; InterruptHandler places held user turns and its
// resumed inference answers them.
func (c *Coordinator) trackOpenExchange(curr *state.LoopState) {
	if hasInterruptMessage(curr.Inputs.UserControlPlaneMessage) {
		c.modelResponseOpen = false
		c.toolBatchesOutstanding = 0
		c.heldTurnNeedsInference = false
	}
	for _, delta := range curr.Inputs.ToolInputDelta {
		if _, ok := delta.Value.(*messages.MessageEndValue); ok && c.toolBatchesOutstanding > 0 {
			c.toolBatchesOutstanding--
		}
	}
	for _, delta := range curr.Inputs.ModelInputDelta {
		if delta.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
			continue
		}
		switch value := delta.Value.(type) {
		case *messages.MessageEndValue:
			c.modelResponseOpen = false
		case *messages.ErrorValue:
			if value.IsTerminal() {
				c.modelResponseOpen = false
			}
		default:
			if opensModelResponse(delta) {
				c.modelResponseOpen = true
			}
		}
	}
}

// opensModelResponse reports whether a model delta is assistant response
// content. Session lifecycle, control and usage events are not, and neither
// is the user's own input transcription, which realtime providers emit as
// TRANSCRIPT deltas with RoleUser. An empty role is the model's, as for
// turn-based providers that do not set it.
func opensModelResponse(delta messages.StreamMessage) bool {
	if delta.Role != "" && delta.Role != messages.RoleAssistant {
		return false
	}
	switch delta.Type { //nolint:exhaustive // Response content opens a response; every other type leaves it as is.
	case messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioStart, messages.StreamTypeAudioDelta, messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart, messages.StreamTypeImageDelta, messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart, messages.StreamTypeVideoDelta, messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart, messages.StreamTypeFileDelta, messages.StreamTypeFileEnd,
		messages.StreamTypeReasoningStart, messages.StreamTypeReasoningDelta, messages.StreamTypeReasoningEnd,
		messages.StreamTypeTranscriptStart, messages.StreamTypeTranscriptDelta, messages.StreamTypeTranscriptEnd,
		messages.StreamTypeRefusal:
		return true
	default:
		return false
	}
}

// holdingUserTurns reports whether a user turn arriving now must wait for
// the open response or outstanding tool batch before joining history.
func (c *Coordinator) holdingUserTurns() bool {
	return c.modelResponseOpen || c.toolBatchesOutstanding > 0
}

// holdUserTurns moves this tick's user messages out of history into the held
// queue. A tick carries one input, so in a user tick they are the tail of
// ConversationBuffer, appended by UpdateWorldHistory.
func (c *Coordinator) holdUserTurns(curr *state.LoopState) {
	buffer := curr.History.ConversationBuffer
	tail := len(buffer) - len(curr.Inputs.UserOutputMessage)
	curr.History.HeldUserMessages = append(curr.History.HeldUserMessages, buffer[tail:]...)
	clear(buffer[tail:])
	curr.History.ConversationBuffer = buffer[:tail]
}

// placeHeldUserTurns records the held user turns, in arrival order, to the
// kernel and to history, so both see them after the exchange they waited for.
func (c *Coordinator) placeHeldUserTurns(ctx context.Context, curr *state.LoopState) {
	PlaceHeldUserMessages(curr, func(message messages.Message) {
		c.sendInferenceResult(ctx, curr, messages.User, message)
	})
}

// PlaceHeldUserMessages appends the held user turns to history in arrival
// order, passing each to record first (the kernel's full-message stream).
// Every path that ends the loop places them, so a turn the user sent is never
// lost from history, whatever ended the exchange it waited for.
func PlaceHeldUserMessages(curr *state.LoopState, record func(messages.Message)) {
	for _, message := range curr.History.HeldUserMessages {
		record(message)
	}
	curr.History.ConversationBuffer = append(curr.History.ConversationBuffer, curr.History.HeldUserMessages...)
	curr.History.HeldUserMessages = nil
}

// writeUserFullMessage records a user message on the kernel's full-message
// stream.
func writeUserFullMessage(ctx context.Context, inbox *messages.TypedBuffer[messages.KernelDeltaRequest], message messages.Message) {
	messages.WriteKernelDelta(ctx, inbox, UserFullMessage(message))
}

// UserFullMessage is the kernel record of a user message.
func UserFullMessage(message messages.Message) messages.KernelDeltaRequest {
	return messages.KernelDeltaRequest{
		Source: messages.User,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue(string(messages.User), message),
		},
	}
}

func (c *Coordinator) resetModelDeltaWindow(curr *state.LoopState) {
	curr.History.ModelDeltaStartIndex = len(curr.History.ConversationDeltaBuffer)
	curr.History.CurrentModelDeltaCount = 0
}

func (c *Coordinator) modelResponseAssemblyForDelta(delta messages.StreamMessage) (*modelResponseAssembly, error) {
	if delta.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return nil, nil
	}
	assembly, err := c.startModelResponse(delta)
	if err != nil || assembly != nil {
		return assembly, err
	}
	return c.modelResponseForDelta(delta)
}

func isTerminalOnlyModelError(delta messages.StreamMessage) bool {
	if delta.Type != messages.StreamTypeMessageEnd {
		return false
	}
	value, ok := delta.Value.(*messages.MessageEndValue)
	return ok && value != nil && (value.Status != "" || value.ProviderErrorCode != "" || value.ProviderErrorMessage != "")
}

func (c *Coordinator) hasSessionCloseControl(curr *state.LoopState) bool {
	for _, msg := range curr.Inputs.UserControlPlaneMessage {
		if cpType := extractControlPlaneType(msg); cpType == messages.ControlPlaneMessageTypeSessionClose || cpType == messages.ControlPlaneMessageTypeStop {
			c.logInfo("Coordinator: session close requested via control plane", logging.Field{Key: "type", Value: string(cpType)})
			return true
		}
	}
	return false
}

func (c *Coordinator) nextToolBatchPass(curr *state.LoopState) int {
	if curr.Mode == state.DuplexSession {
		return c.ensureSessionPass(curr)
	}
	curr.History.CurrentPassID++
	return curr.History.CurrentPassID
}

func (c *Coordinator) nextToolContinuationPass(curr *state.LoopState) int {
	if curr.Mode == state.DuplexSession {
		return c.ensureSessionPass(curr)
	}
	curr.History.CurrentPassID++
	return curr.History.CurrentPassID
}

func (c *Coordinator) ensureSessionPass(curr *state.LoopState) int {
	if curr.History.CurrentPassID == 0 {
		curr.History.CurrentPassID = 1
	}
	return curr.History.CurrentPassID
}

// replaceEngineModelOutputs swaps the engine's single-window reconstruction
// for completed response-scoped assemblies while retaining trace metadata.
func (c *Coordinator) replaceEngineModelOutputs(curr *state.LoopState, completed []messages.Message) {
	engineOutputs := curr.Inputs.ModelOutputMessage
	if c.engineOutputsAtHistoryTail(curr, engineOutputs) {
		curr.History.ConversationBuffer = curr.History.ConversationBuffer[:len(curr.History.ConversationBuffer)-len(engineOutputs)]
	}
	curr.Inputs.ModelOutputMessage = curr.Inputs.ModelOutputMessage[:0]
	for index, message := range completed {
		if index < len(engineOutputs) {
			copyModelMetadata(&message, engineOutputs[index])
		}
		curr.Inputs.ModelOutputMessage = append(curr.Inputs.ModelOutputMessage, message)
		curr.History.ConversationBuffer = append(curr.History.ConversationBuffer, message)
	}
}

func (c *Coordinator) engineOutputsAtHistoryTail(curr *state.LoopState, outputs []messages.Message) bool {
	if len(outputs) == 0 || len(curr.History.ConversationBuffer) < len(outputs) {
		return false
	}
	start := len(curr.History.ConversationBuffer) - len(outputs)
	for index, message := range outputs {
		historyMessage := curr.History.ConversationBuffer[start+index]
		if historyMessage.GlobalIndex != message.GlobalIndex || historyMessage.ActorID != message.ActorID {
			return false
		}
	}
	return true
}

func copyModelMetadata(target *messages.Message, source messages.Message) {
	target.GlobalIndex = source.GlobalIndex
	target.ActorProvidedID = source.ActorProvidedID
	target.ActorProvidedIndex = source.ActorProvidedIndex
	target.ActorStreamID = source.ActorStreamID
	target.ActorID = source.ActorID
}
