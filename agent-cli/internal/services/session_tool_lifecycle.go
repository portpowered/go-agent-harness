package services

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// SessionScheduledAudioClassification identifies a terminal session failure
	// caused by a configured scheduled input or its assistant response not
	// completing before shutdown.
	SessionScheduledAudioClassification = "scheduled_audio_incomplete"
	// SessionUnresolvedToolResultClassification identifies a terminal session
	// failure caused by a result that never reached the provider-facing send
	// boundary.
	SessionUnresolvedToolResultClassification = "unresolved_tool_result"
	// SessionImageContinuationClassification identifies a terminal session
	// failure after a read_image result was accepted but its model continuation
	// never reached a terminal response.
	SessionImageContinuationClassification = "image_tool_continuation"
	// SessionToolContinuationClassification identifies a terminal session
	// failure after an ordinary tool result was accepted but its grounded model
	// continuation never reached a terminal response.
	SessionToolContinuationClassification = "tool_continuation"
)

var (
	// ErrSessionUnresolvedToolResults is the stable sentinel for a session that
	// terminated while one or more provider-requested tool results were still
	// undelivered.
	ErrSessionUnresolvedToolResults = errors.New("session ended with unresolved tool results")
	// ErrSessionImageContinuationIncomplete is the stable sentinel for a
	// read_image result that reached the provider but did not receive its
	// follow-up model response before the session terminated.
	ErrSessionImageContinuationIncomplete = errors.New("session ended before the image tool continuation")
	// ErrSessionToolContinuationIncomplete is the stable sentinel for any
	// non-image tool result that reached the provider but did not receive its
	// follow-up model response before the session terminated.
	ErrSessionToolContinuationIncomplete = errors.New("session ended before the tool continuation")
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

// SessionImageContinuationError carries the read_image call IDs whose result
// was accepted but whose post-tool model response did not complete with
// observable output. ProviderStatuses, ProviderCodes, and ProviderDetails
// retain bounded, sanitized terminal context when the provider authored a
// failed response.
// It is deliberately separate from SessionUnresolvedToolResultsError:
// provider acceptance is not conversation completion.
type SessionImageContinuationError struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

func (e *SessionImageContinuationError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrSessionImageContinuationIncomplete.Error()
	}
	return fmt.Sprintf("image tool continuation was not completed for %d call(s): %s", len(e.CallIDs), formatContinuationFailureIDs(e.CallIDs, e.ProviderStatuses, e.ProviderCodes, e.ProviderDetails))
}

func (e *SessionImageContinuationError) Unwrap() error {
	return ErrSessionImageContinuationIncomplete
}

// SessionToolContinuationError carries ordinary tool call IDs whose accepted
// result still lacks a terminal model continuation. CallIDs is expected to be
// sorted and deduplicated by the observer snapshot. ProviderStatuses and
// ProviderCodes and ProviderDetails retain bounded, sanitized terminal context
// when available.
type SessionToolContinuationError struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

func (e *SessionToolContinuationError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrSessionToolContinuationIncomplete.Error()
	}
	return fmt.Sprintf("tool continuation was not completed for %d call(s): %s", len(e.CallIDs), formatContinuationFailureIDs(e.CallIDs, e.ProviderStatuses, e.ProviderCodes, e.ProviderDetails))
}

func (e *SessionToolContinuationError) Unwrap() error {
	return ErrSessionToolContinuationIncomplete
}

func formatContinuationFailureIDs(ids []string, statuses, codes, details map[string]string) string {
	formatted := make([]string, 0, len(ids))
	for _, id := range ids {
		annotations := make([]string, 0, 3)
		if status := strings.TrimSpace(statuses[id]); status != "" {
			annotations = append(annotations, "status="+status)
		}
		if code := strings.TrimSpace(codes[id]); code != "" {
			annotations = append(annotations, "code="+code)
		}
		if detail := strings.TrimSpace(details[id]); detail != "" {
			annotations = append(annotations, "detail="+detail)
		}
		if len(annotations) == 0 {
			formatted = append(formatted, id)
			continue
		}
		formatted = append(formatted, fmt.Sprintf("%s (%s)", id, strings.Join(annotations, "; ")))
	}
	return strings.Join(formatted, ", ")
}

func formatContinuationMetadata(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+"="+values[id])
	}
	return strings.Join(parts, ", ")
}

// withPendingToolContinuations preserves any primary provider, cancellation,
// or timeout cause while adding the typed continuation failure once. Image
// calls retain their more specific existing error so callers do not receive
// two lifecycle errors for the same read_image obligation.
func withPendingToolContinuations(err error, observer *sessionProgressObserver) error {
	if observer == nil {
		return err
	}
	ids, statuses, codes, details := observer.pendingNonImageToolContinuationSnapshot()
	if len(ids) == 0 {
		return err
	}
	var existing *SessionToolContinuationError
	if errors.As(err, &existing) {
		return err
	}
	continuation := &SessionToolContinuationError{CallIDs: ids, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}
	if err == nil {
		return continuation
	}
	return errors.Join(err, continuation)
}

// withPendingImageContinuations preserves any primary provider, cancellation,
// or timeout cause while adding the typed continuation failure once.
func withPendingImageContinuations(err error, observer *sessionProgressObserver) error {
	if observer == nil {
		return err
	}
	ids, statuses, codes, details := observer.pendingImageContinuationSnapshot()
	if len(ids) == 0 {
		return err
	}
	var existing *SessionImageContinuationError
	if errors.As(err, &existing) {
		return err
	}
	continuation := &SessionImageContinuationError{CallIDs: ids, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}
	if err == nil {
		return continuation
	}
	return errors.Join(err, continuation)
}
