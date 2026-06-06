package engine

import "github.com/portpowered/go-agent-loop/pkg/state"

// ExecutionMode is a type alias for state.ExecutionMode. The canonical declaration
// lives in the state package; this alias keeps existing engine-package callers working.
type ExecutionMode = state.ExecutionMode

const (
	// ModeAskOnce sends one command and waits for a complete response.
	ModeAskOnce = state.ModeAskOnce
	// ModeStreaming sends one command and streams the response incrementally.
	ModeStreaming = state.ModeStreaming
	// ModeTurnTaking allows alternating user and agent turns.
	ModeTurnTaking = state.ModeTurnTaking
	// DuplexSession maintains a persistent bidirectional session (e.g. audio inference).
	// Auto-termination on final response is suppressed; termination comes from
	// control plane (session_close or stop) only.
	DuplexSession = state.DuplexSession
)
