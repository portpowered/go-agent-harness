// Package terminaloutcome owns the host-neutral terminal publication contract
// used by session runtimes and replay consumers.
package terminaloutcome

import (
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Sentinel is an immutable error value suitable for errors.Is identity.
type Sentinel string

func (s Sentinel) Error() string { return string(s) }

const (
	// ErrSessionTerminalAlreadyPublished identifies a second attempt to cross
	// the single terminal publication boundary.
	ErrSessionTerminalAlreadyPublished Sentinel = "session terminal already published"
	// ErrSessionMaxDurationExpired is the runtime-neutral duration sentinel.
	// Hosts with a legacy sentinel may pass that sentinel to
	// Service.HasIndependentFailure as an ignored error.
	ErrSessionMaxDurationExpired Sentinel = "session max duration expired"
)

// Service constructs isolated reporters. A service has no invocation state;
// every call to NewReporter returns a fresh bounded state machine.
type Service interface {
	NewReporter() Reporter
	// HasIndependentFailure classifies a run error without exposing the
	// implementation's error traversal. ignored errors are lifecycle sentinels
	// that do not constitute an independent failure for the caller's host.
	HasIndependentFailure(error, ...error) bool
}

// Reporter records terminal evidence and crosses the customer-facing output
// boundary exactly once. Implementations keep synchronization, precedence,
// normalization, and rendering private.
type Reporter interface {
	MarkRunStarted()
	ObserveStreamMessage(messages.StreamMessage, bool)
	MarkDurationExpiry(messages.TerminalOutputState)
	MarkReplayComplete()
	RecordArtifactFinalization(bool, error)
	Publish(io.Writer, error) error
}
