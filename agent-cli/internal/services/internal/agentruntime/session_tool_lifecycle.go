package agentruntime

import (
	"errors"
	sessionpublic "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	sc "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
	w "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation/wire"
)

const (
	SessionScheduledAudioClassification       = "scheduled_audio_incomplete"
	SessionUnresolvedToolResultClassification = "unresolved_tool_result"
	SessionImageContinuationClassification    = "image_tool_continuation"
	SessionToolContinuationClassification     = "tool_continuation"
	ErrSessionAudioResponseIncomplete         = sc.ErrAudioResponseIncomplete
)

var ErrSessionUnresolvedToolResults = sessionpublic.ErrSessionUnresolvedToolResults
var ErrSessionImageContinuationIncomplete = sc.ErrImageContinuationIncomplete
var ErrSessionToolContinuationIncomplete = sc.ErrToolContinuationIncomplete

// Deprecated: use sessioncontinuation.UnresolvedToolResultsError.
type SessionUnresolvedToolResultsError = sessionpublic.SessionUnresolvedToolResultsError

// Compatibility alias (deprecated; use sessioncontinuation.ImageContinuationError).
type SessionImageContinuationError = sc.LegacyImageContinuationError

// Compatibility alias (deprecated; use sessioncontinuation.ToolContinuationError).
type SessionToolContinuationError = sc.LegacyToolContinuationError

func joinSessionAudioOutputError(a error, p string, o error) error {
	return w.New().JoinAudioOutputError(a, p, o)
}

func withUnresolvedToolResults(err error, observer *sessionProgressObserver) error {
	if errors.Is(err, sessionpublic.ErrSessionUnresolvedToolResults) {
		return err
	}
	ids, statuses := observer.unresolvedToolCallIDs(), observer.unresolvedToolResultSendStatuses()
	next := w.New().Enrich(err, sc.Snapshot{Unresolved: sc.UnresolvedToolResultsSnapshot{CallIDs: ids, SendStatuses: statuses}})
	if len(ids) == 0 {
		return next
	}
	return errors.Join(next, sessionpublic.NewSessionUnresolvedToolResultsError(ids, statuses))
}

func formatContinuationMetadata(v map[string]string) string { return w.New().FormatMetadata(v) }

func withPendingToolContinuations(err error, observer *sessionProgressObserver) error {
	ids, statuses, codes, details := observer.pendingNonImageToolContinuationSnapshot()
	return w.New().Enrich(err, sc.Snapshot{Tool: sc.ContinuationSnapshot{CallIDs: ids, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}})
}

func withPendingImageContinuations(err error, observer *sessionProgressObserver) error {
	ids, statuses, codes, details := observer.pendingImageContinuationSnapshot()
	return w.New().Enrich(err, sc.Snapshot{Image: sc.ContinuationSnapshot{CallIDs: ids, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}})
}
