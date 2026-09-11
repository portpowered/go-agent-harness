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
		c.kernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
			Source: messages.Model,
			Delta:  delta,
		})
	}

	for _, delta := range curr.Inputs.ToolInputDelta {
		c.logInfo("CoordinatorDelta: tool delta", logging.Field{Key: "delta", Value: delta})
		c.kernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
			Source: messages.Tool,
			Delta:  delta,
		})
	}

	for _, delta := range curr.Inputs.UserInputDelta {
		c.logInfo("CoordinatorDelta: user delta", logging.Field{Key: "delta", Value: delta})
		c.kernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
			Source: messages.User,
			Delta:  delta,
		})
	}

	if curr.Inputs.TerminateLoop {
		c.logInfo("CoordinatorDelta: terminating loop", logging.Field{Key: "terminateLoop", Value: curr.Inputs.TerminateLoop})

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
			c.kernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
				Source: messages.System,
				Delta: messages.StreamMessage{
					Type:  messages.StreamTypeSessionClose,
					Value: newLoopSessionCloseValue(curr.SessionID, reason),
				},
			})
		}

		c.kernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
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

// extractSessionCloseReason returns a reason string from a control plane message.
func extractSessionCloseReason(msg messages.Message) string {
	for _, part := range msg.ContentParts {
		if cp, ok := part.(messages.ControlPlanePart); ok {
			switch cp.ControlPlaneMessageType {
			case messages.ControlPlaneMessageTypeSessionClose:
				return "client_close"
			case messages.ControlPlaneMessageTypeStop:
				return "stop"
			}
		}
	}
	return ""
}

func newLoopSessionCloseValue(sessionID, reason string) *messages.SessionCloseValue {
	terminalReason := messages.TerminalReasonSessionClose
	if reason == "stop" {
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
