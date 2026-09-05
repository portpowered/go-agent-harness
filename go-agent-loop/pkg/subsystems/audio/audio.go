// Package audio connects the tick loop to memory-only audio endpoints.
// Media workers run independently of Execute; no device/provider operations
// or PCM processing occur on a reasoning tick.
package audio

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
	media "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// ErrMissingPorts identifies a subsystem that was constructed without the
// runtime-owned media capabilities it needs. A missing capability is a wiring
// error; callers must not silently replace it with host-time or detached
// buffers.
var ErrMissingPorts = errors.New("audio subsystem ports are incomplete")

// BufferPort is the observation capability for one runtime-owned memory
// buffer. It deliberately exposes no device/provider operation.
type BufferPort interface {
	Snapshot() media.BufferStats
}

// CommandPort is the nonblocking control capability consumed by an external
// media worker. Keeping this as a small interface allows a runtime to expose
// its actual command queue without copying or detaching media buffers.
type CommandPort interface {
	TrySubmit(media.Command) error
}

// Ports contain memory-only capabilities, not caller-implemented I/O
// interfaces. Capture/Playback are observation handles for runtime-owned
// buffers; Commands is consumed by an external worker.
type Ports struct {
	Capture  BufferPort
	Playback BufferPort
	Commands CommandPort
}

type Subsystem struct {
	ports     Ports
	passID    uint64
	commandID uint64
}

func New(ports Ports) *Subsystem { return &Subsystem{ports: ports} }

var _ subsystems.Subsystem = (*Subsystem)(nil)

// Run after the interrupt handler and before coordinator (group 1).
func (*Subsystem) TickGroup() subsystems.TickGroup { return subsystems.TickGroupToolResultForwarder }

func (s *Subsystem) Execute(ctx context.Context, curr *state.LoopState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.ports.Capture == nil && s.ports.Playback == nil && s.ports.Commands == nil {
		return ErrMissingPorts
	}
	pass := uint64(curr.History.CurrentPassID)
	if pass > s.passID {
		// Pass changes alone also include normal turns, so only queue a
		// playback interrupt when an explicit interrupt control arrived.
		for _, msg := range curr.Inputs.UserControlPlaneMessage {
			if isInterrupt(msg) {
				var playbackEpoch uint64
				if s.ports.Playback != nil {
					playbackEpoch = s.ports.Playback.Snapshot().Epoch
				}
				if s.ports.Commands != nil {
					s.commandID++
					// Epoch is owned by the playback worker. The loop pass only
					// identifies this scheduling pass and must never be used as a
					// device generation. With no playback port, zero asks the
					// worker to apply an unconditional discard.
					epoch := uint64(0)
					if s.ports.Playback != nil {
						epoch = playbackEpoch + 1
					}
					if err := s.ports.Commands.TrySubmit(media.Command{ID: s.commandID, Epoch: epoch, Kind: media.CommandInterrupt}); err != nil {
						return fmt.Errorf("queue audio interrupt: %w", err)
					}
				}
				break
			}
		}
		s.passID = pass
	}
	var capture media.BufferStats
	if s.ports.Capture != nil {
		capture = s.ports.Capture.Snapshot()
	}
	var playback media.BufferStats
	if s.ports.Playback != nil {
		playback = s.ports.Playback.Snapshot()
	}
	curr.Audio = &state.AudioState{Capture: capture, Playback: playback, LastCommandID: s.commandID}
	return nil
}

func isInterrupt(msg messages.Message) bool {
	for _, part := range msg.ContentParts {
		if control, ok := part.(messages.ControlPlanePart); ok && control.ControlPlaneMessageType == messages.ControlPlaneMessageTypeInterrupt {
			return true
		}
	}
	return false
}
