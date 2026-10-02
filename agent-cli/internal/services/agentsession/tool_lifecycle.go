package agentsession

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// ErrSessionUnresolvedToolResults identifies a session that stopped before one
// or more provider-requested tool results crossed the provider-facing send
// boundary.
const ErrSessionUnresolvedToolResults = sessiontrace.ErrUnresolvedToolResults

// SessionUnresolvedToolResultsError carries the provider call IDs that were
// still outstanding at a terminal session boundary. CallIDs is deduplicated
// and lexically ordered. SendStatuses retains the first observable non-success
// result-send outcome when the provider session exposed one.
type SessionUnresolvedToolResultsError = sessiontrace.UnresolvedToolResultsError
