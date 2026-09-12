//go:build wireinject
// +build wireinject

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
)

// NewParticipantLifecycle is the dedicated Wire entry point for the
// invocation-scoped participant state machine.
func NewParticipantLifecycle(options rooms.ParticipantLifecycleOptions) rooms.ParticipantLifecycle {
	wire.Build(lifecycle.NewParticipantLifecycle)
	return nil
}

// NewTrackedSession is the dedicated Wire entry point for session ownership
// and selective post-bound admission.
func NewTrackedSession(session messages.Session, participant rooms.ParticipantLifecycle, admissionClosed <-chan struct{}) rooms.TrackedSession {
	wire.Build(lifecycle.NewTrackedSession)
	return nil
}

// NewConnectionTracker is the dedicated Wire entry point for the first
// connection outcome and transport cleanup boundary.
func NewConnectionTracker(inner messages.SessionInferencer, participant rooms.ParticipantLifecycle, admissionClosed <-chan struct{}) rooms.ParticipantConnectionTracker {
	wire.Build(lifecycle.NewConnectionTracker)
	return nil
}
