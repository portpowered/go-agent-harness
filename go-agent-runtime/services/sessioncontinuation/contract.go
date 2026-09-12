// Package sessioncontinuation owns the host-neutral error and snapshot
// contract for provider tool-result continuations.
package sessioncontinuation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

type lifecycleSentinel string

func (e lifecycleSentinel) Error() string { return string(e) }

// These immutable sentinels give continuation consumers a focused package to
// depend on while the typed errors retain compatibility with session errors.
const (
	ErrUnresolvedToolResults                                = runtimesession.ErrLiveUnresolvedToolResults
	ErrSessionUnresolvedToolResults                         = ErrUnresolvedToolResults
	ErrImageContinuationIncomplete        lifecycleSentinel = "session ended before the image tool continuation"
	ErrSessionImageContinuationIncomplete                   = ErrImageContinuationIncomplete
	ErrToolContinuationIncomplete         lifecycleSentinel = "session ended before the tool continuation"
	ErrSessionToolContinuationIncomplete                    = ErrToolContinuationIncomplete
)

// ErrAudioResponseIncomplete is a compatibility alias for the finite audio
// response cause that can be joined with a continuation error.
const ErrAudioResponseIncomplete = runtimesession.ErrLiveAudioResponseIncomplete

// SendAttempt is one ordered provider-facing result-send observation. The
// service retains the first non-success status for each call ID.
type SendAttempt struct {
	CallID string
	Status messages.SessionSendStatus
}

// UnresolvedToolResultsSnapshot contains provider calls whose local results
// did not cross the send boundary. Inputs are copied and normalized by Service.
type UnresolvedToolResultsSnapshot struct {
	CallIDs      []string
	SendStatuses map[string]messages.SessionSendStatus
	SendAttempts []SendAttempt
}

// ContinuationSnapshot contains accepted result calls awaiting a model
// continuation and bounded provider metadata for their terminal state.
type ContinuationSnapshot struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

// Snapshot is the complete immutable input to one terminal enrichment.
// Image calls take precedence over ordinary tool calls with the same ID.
type Snapshot struct {
	Unresolved UnresolvedToolResultsSnapshot
	Tool       ContinuationSnapshot
	Image      ContinuationSnapshot
}

// Service normalizes observer values and joins typed lifecycle causes onto a
// primary error without reading host, provider, device, or process state.
type Service interface {
	Normalize(Snapshot) Snapshot
	Enrich(error, Snapshot) error
	FormatMetadata(map[string]string) string
	JoinAudioOutputError(error, string, error) error
}

// UnresolvedToolResultsError carries a deterministic owned snapshot of
// unresolved provider call IDs and their first non-success send statuses.
type UnresolvedToolResultsError struct {
	CallIDs      []string
	SendStatuses map[string]messages.SessionSendStatus
}

// SessionUnresolvedToolResultsError is the descriptive compatibility name.
type SessionUnresolvedToolResultsError = UnresolvedToolResultsError

func (e *UnresolvedToolResultsError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrUnresolvedToolResults.Error()
	}
	message := fmt.Sprintf("tool results were not delivered for %d unresolved call(s): %s", len(e.CallIDs), strings.Join(e.CallIDs, ", "))
	statusParts := make([]string, 0, len(e.SendStatuses))
	for _, id := range e.CallIDs {
		if status := e.SendStatuses[id]; status != "" {
			statusParts = append(statusParts, fmt.Sprintf("%s=%s", id, status))
		}
	}
	if len(statusParts) > 0 {
		message += " (send outcomes: " + strings.Join(statusParts, ", ") + ")"
	}
	return message
}

func (e *UnresolvedToolResultsError) Unwrap() error { return ErrUnresolvedToolResults }

// UnresolvedCallIDs returns an owned, lexically ordered ID snapshot.
func (e *UnresolvedToolResultsError) UnresolvedCallIDs() []string {
	if e == nil {
		return nil
	}
	return append([]string(nil), e.CallIDs...)
}

// SendStatusSnapshot returns an owned status map for diagnostics and tests.
func (e *UnresolvedToolResultsError) SendStatusSnapshot() map[string]messages.SessionSendStatus {
	if e == nil {
		return nil
	}
	statuses := make(map[string]messages.SessionSendStatus, len(e.SendStatuses))
	for id, status := range e.SendStatuses {
		statuses[id] = status
	}
	return statuses
}

// ImageContinuationError is the continuation error type shared with the
// session service. Its fields are populated from copied snapshots by Service.
type ImageContinuationError struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

func (e *ImageContinuationError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrImageContinuationIncomplete.Error()
	}
	return fmt.Sprintf("image tool continuation was not completed for %d call(s): %s", len(e.CallIDs), formatContinuationFailureIDs(e.CallIDs, e.ProviderStatuses, e.ProviderCodes, e.ProviderDetails))
}

func (e *ImageContinuationError) Unwrap() error { return ErrImageContinuationIncomplete }

func (e *ImageContinuationError) Is(target error) bool {
	return target == ErrImageContinuationIncomplete || target == runtimesession.ErrLiveImageContinuationIncomplete
}

// SessionImageContinuationError is the descriptive compatibility name.
type SessionImageContinuationError = ImageContinuationError

// ToolContinuationError is the ordinary tool continuation error type.
type ToolContinuationError struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

func (e *ToolContinuationError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrToolContinuationIncomplete.Error()
	}
	return fmt.Sprintf("tool continuation was not completed for %d call(s): %s", len(e.CallIDs), formatContinuationFailureIDs(e.CallIDs, e.ProviderStatuses, e.ProviderCodes, e.ProviderDetails))
}

func (e *ToolContinuationError) Unwrap() error { return ErrToolContinuationIncomplete }

func (e *ToolContinuationError) Is(target error) bool {
	return target == ErrToolContinuationIncomplete || target == runtimesession.ErrLiveToolContinuationIncomplete
}

// SessionToolContinuationError is the descriptive compatibility name.
type SessionToolContinuationError = ToolContinuationError

func formatContinuationFailureIDs(ids []string, statuses, codes, details map[string]string) string {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	formatted := make([]string, 0, len(ordered))
	for _, id := range ordered {
		if strings.TrimSpace(id) == "" {
			continue
		}
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
