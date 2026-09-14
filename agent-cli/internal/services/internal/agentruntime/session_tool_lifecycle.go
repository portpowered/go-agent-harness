package agentruntime

import (
	sc "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
	w "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation/wire"
)

const (
	// ErrSessionAudioResponseIncomplete is the CLI compatibility name for the
	// reusable runtime's finite audio-response contract.
	ErrSessionAudioResponseIncomplete = sc.ErrAudioResponseIncomplete
)

var (
	ErrSessionUnresolvedToolResults       = sc.ErrSessionUnresolvedToolResults
	ErrSessionImageContinuationIncomplete = sc.ErrImageContinuationIncomplete
	ErrSessionToolContinuationIncomplete  = sc.ErrToolContinuationIncomplete
)

// Deprecated: use sessioncontinuation.UnresolvedToolResultsError.
type SessionUnresolvedToolResultsError = sc.UnresolvedToolResultsError

// Deprecated: use sessioncontinuation.ImageContinuationError.
type SessionImageContinuationError = sc.ImageContinuationError

// Deprecated: use sessioncontinuation.ToolContinuationError.
type SessionToolContinuationError = sc.ToolContinuationError

func joinSessionAudioOutputError(a error, p string, o error) error {
	return w.New().JoinAudioOutputError(a, p, o)
}

func withUnresolvedToolResults(err error, observer *sessionProgressObserver) error {
	ids, statuses := observer.unresolvedToolCallIDs(), observer.unresolvedToolResultSendStatuses()
	return w.New().Enrich(err, sc.Snapshot{Unresolved: sc.UnresolvedToolResultsSnapshot{CallIDs: ids, SendStatuses: statuses}})
}

func withPendingToolContinuations(err error, observer *sessionProgressObserver) error {
	ids, statuses, codes, details := observer.pendingNonImageToolContinuationSnapshot()
	return w.New().Enrich(err, sc.Snapshot{Tool: sc.ContinuationSnapshot{CallIDs: ids, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}})
}

func withPendingImageContinuations(err error, observer *sessionProgressObserver) error {
	ids, statuses, codes, details := observer.pendingImageContinuationSnapshot()
	return w.New().Enrich(err, sc.Snapshot{Image: sc.ContinuationSnapshot{CallIDs: ids, ProviderStatuses: statuses, ProviderCodes: codes, ProviderDetails: details}})
}
