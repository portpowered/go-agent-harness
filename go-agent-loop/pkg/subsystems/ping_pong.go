package subsystems

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// PingPong responds to ping control plane messages with PONG stream events.
// It is only active in DuplexSession; in other modes Execute is a no-op.
type PingPong struct {
	kernelDeltaInbox *messages.TypedBuffer[messages.KernelDeltaRequest]
	logger           logging.Logger
}

// NewPingPong creates a PingPong subsystem that writes pong events to the
// given kernel delta inbox.
func NewPingPong(kernelDeltaInbox *messages.TypedBuffer[messages.KernelDeltaRequest], logger logging.Logger) *PingPong {
	return &PingPong{
		kernelDeltaInbox: kernelDeltaInbox,
		logger:           logger,
	}
}

var _ Subsystem = (*PingPong)(nil)

// Execute implements [Subsystem]. In DuplexSession, it checks for ping control
// plane messages and emits a PONG stream event for each. In other modes it
// is a no-op.
func (p *PingPong) Execute(ctx context.Context, curr *state.LoopState) error {
	if curr.Mode != state.DuplexSession {
		return nil
	}

	for _, msg := range curr.Inputs.UserControlPlaneMessage {
		if !isPingMessage(msg) {
			continue
		}

		if p.logger != nil {
			p.logger.Debug("PingPong: responding to ping")
		}

		p.kernelDeltaInbox.Write(ctx, messages.KernelDeltaRequest{
			Source: messages.System,
			Delta: messages.StreamMessage{
				Type:  messages.StreamTypePong,
				Value: messages.NewPongValue(time.Now().UnixMilli()),
			},
		})
	}

	return nil
}

// TickGroup implements [Subsystem].
func (p *PingPong) TickGroup() TickGroup {
	return TickGroupPingPong
}

// isPingMessage returns true if the message contains a ping ControlPlanePart.
func isPingMessage(msg messages.Message) bool {
	for _, part := range msg.ContentParts {
		if cp, ok := part.(messages.ControlPlanePart); ok {
			if cp.ControlPlaneMessageType == messages.ControlPlaneMessageTypePing {
				return true
			}
		}
	}
	return false
}
