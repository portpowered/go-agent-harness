package sessionterminal

import (
	"fmt"
	"strings"
)

// ErrUnresolvedToolResults identifies provider-requested tool results that did
// not cross the provider-facing send boundary before session termination.
const ErrUnresolvedToolResults unresolvedToolResultsError = "session ended with unresolved tool results"

type unresolvedToolResultsError string

func (e unresolvedToolResultsError) Error() string { return string(e) }

// UnresolvedToolResultsError carries deterministic call IDs and any observed
// provider send outcomes for a terminal session.
type UnresolvedToolResultsError struct {
	CallIDs      []string
	SendStatuses map[string]string
}

func (e *UnresolvedToolResultsError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrUnresolvedToolResults.Error()
	}
	message := fmt.Sprintf("tool results were not delivered for %d unresolved call(s): %s", len(e.CallIDs), strings.Join(e.CallIDs, ", "))
	statusParts := make([]string, 0, len(e.SendStatuses))
	for _, id := range e.CallIDs {
		if status := strings.TrimSpace(e.SendStatuses[id]); status != "" {
			statusParts = append(statusParts, fmt.Sprintf("%s=%s", id, status))
		}
	}
	if len(statusParts) > 0 {
		message += " (send outcomes: " + strings.Join(statusParts, ", ") + ")"
	}
	return message
}

func (e *UnresolvedToolResultsError) Unwrap() error { return ErrUnresolvedToolResults }

func (e *UnresolvedToolResultsError) UnresolvedCallIDs() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.CallIDs...)
}
