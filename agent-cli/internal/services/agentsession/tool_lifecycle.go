package agentsession

import (
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrSessionUnresolvedToolResults identifies a session that stopped before one
// or more provider-requested tool results crossed the provider-facing send
// boundary.
const ErrSessionUnresolvedToolResults = sessionUnresolvedToolResultsError("session ended with unresolved tool results")

type sessionUnresolvedToolResultsError string

func (e sessionUnresolvedToolResultsError) Error() string { return string(e) }

// SessionUnresolvedToolResultsError carries the provider call IDs that were
// still outstanding at a terminal session boundary. CallIDs is deduplicated
// and lexically ordered. SendStatuses retains the first observable non-success
// result-send outcome when the provider session exposed one.
type SessionUnresolvedToolResultsError struct {
	CallIDs      []string
	SendStatuses map[string]messages.SessionSendStatus
}

// NewSessionUnresolvedToolResultsError constructs a deterministic unresolved
// result error from provider call IDs and optional send outcomes.
func NewSessionUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *SessionUnresolvedToolResultsError {
	ordered := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)

	ownedStatuses := make(map[string]messages.SessionSendStatus, len(statuses))
	for _, id := range ordered {
		if status, ok := statuses[id]; ok {
			ownedStatuses[id] = status
		}
	}
	return &SessionUnresolvedToolResultsError{CallIDs: ordered, SendStatuses: ownedStatuses}
}

func (e *SessionUnresolvedToolResultsError) Error() string {
	if e == nil {
		return ErrSessionUnresolvedToolResults.Error()
	}
	ids := e.UnresolvedCallIDs()
	if len(ids) == 0 {
		return ErrSessionUnresolvedToolResults.Error()
	}

	message := fmt.Sprintf("tool results were not delivered for %d unresolved call(s): %s", len(ids), strings.Join(ids, ", "))
	statusParts := make([]string, 0, len(e.SendStatuses))
	for _, id := range ids {
		if status, ok := e.SendStatuses[id]; ok && status != "" {
			statusParts = append(statusParts, fmt.Sprintf("%s=%s", id, status))
		}
	}
	if len(statusParts) > 0 {
		message += " (send outcomes: " + strings.Join(statusParts, ", ") + ")"
	}
	return message
}

func (e *SessionUnresolvedToolResultsError) Unwrap() error {
	return ErrSessionUnresolvedToolResults
}

// UnresolvedCallIDs returns an owned, lexically ordered ID snapshot.
func (e *SessionUnresolvedToolResultsError) UnresolvedCallIDs() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.CallIDs...)
}
