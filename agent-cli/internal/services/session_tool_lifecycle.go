package services

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// SessionUnresolvedToolResultClassification identifies a terminal session
	// failure caused by a result that never reached the provider-facing send
	// boundary.
	SessionUnresolvedToolResultClassification = "unresolved_tool_result"
)

var (
	// ErrSessionUnresolvedToolResults is the stable sentinel for a session that
	// terminated while one or more provider-requested tool results were still
	// undelivered.
	ErrSessionUnresolvedToolResults = errors.New("session ended with unresolved tool results")
)

// SessionUnresolvedToolResultsError carries the provider call IDs that were
// still outstanding when a session reached a terminal path. CallIDs is always
// deduplicated and lexically ordered. SendStatuses records the first observable
// non-success outcome for a result send, when the provider session exposed one.
type SessionUnresolvedToolResultsError struct {
	CallIDs      []string
	SendStatuses map[string]messages.SessionSendStatus
}

func newSessionUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *SessionUnresolvedToolResultsError {
	ordered := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
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

// withUnresolvedToolResults adds the stable typed lifecycle error once. The
// original terminal cause remains available through errors.Is/errors.As when
// the two errors are joined.
func withUnresolvedToolResults(err error, observer *sessionProgressObserver) error {
	if observer == nil {
		return err
	}
	ids := observer.unresolvedToolCallIDs()
	if len(ids) == 0 {
		return err
	}
	var existing *SessionUnresolvedToolResultsError
	if errors.As(err, &existing) {
		return err
	}
	unresolved := newSessionUnresolvedToolResultsError(ids, observer.unresolvedToolResultSendStatuses())
	if err == nil {
		return unresolved
	}
	return errors.Join(err, unresolved)
}
