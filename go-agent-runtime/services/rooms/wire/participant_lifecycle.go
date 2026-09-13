package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
)

// NewParticipantLifecycle is the dedicated rooms composition entry point for
// the invocation-scoped participant state machine. The rooms package keeps
// providers.go as the sole Wire injector; these small adapters keep the
// participant constructors available from the same host-neutral composition
// boundary without creating a second generator input.
func NewParticipantLifecycle(options rooms.ParticipantLifecycleOptions) rooms.ParticipantLifecycle {
	return lifecycle.NewParticipantLifecycle(options)
}

// NewTrackedSession keeps session ownership and selective post-bound admission
// behind the rooms composition boundary.
func NewTrackedSession(session messages.Session, participant rooms.ParticipantLifecycle, admissionClosed <-chan struct{}) rooms.TrackedSession {
	return lifecycle.NewTrackedSession(session, participant, admissionClosed)
}

// NewConnectionTracker keeps the first connection outcome and transport
// cleanup boundary behind the rooms composition boundary.
func NewConnectionTracker(inner messages.SessionInferencer, participant rooms.ParticipantLifecycle, admissionClosed <-chan struct{}) rooms.ParticipantConnectionTracker {
	return lifecycle.NewConnectionTracker(inner, participant, admissionClosed)
}
