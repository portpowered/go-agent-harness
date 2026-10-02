package agentsession

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
)

// NewSessionUnresolvedToolResultsError constructs a deterministic unresolved
// result error from provider call IDs and optional send outcomes.
func NewSessionUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *SessionUnresolvedToolResultsError {
	return sessiontracewire.NewUnresolvedToolResultsError(ids, statuses)
}
