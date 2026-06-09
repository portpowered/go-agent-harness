package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/state"
)

// CoordinatorDelta consumes streaming delta messages from the current tick inputs
// and dispatches them to the kernel participant's delta inbox for IO delivery.
type CoordinatorDelta struct {
	kernelDeltaInbox messages.TypedBuffer[messages.KernelDeltaRequest]
	logger           logging.Logger
}

func NewCoordinatorDelta(kernelDeltaInbox messages.TypedBuffer[messages.KernelDeltaRequest], logger logging.Logger) *CoordinatorDelta {
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
