package agentruntime

import (
	"errors"
	"fmt"

	sessionpublic "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessioncontract "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

var (
	// ErrSessionUnresolvedToolResults is the stable sentinel for a session that
	// terminated while one or more provider-requested tool results were still
	// undelivered.
	ErrSessionUnresolvedToolResults       = sessionpublic.ErrSessionUnresolvedToolResults
	ErrSessionImageContinuationIncomplete = sessioncontract.ErrLiveImageContinuationIncomplete
	ErrSessionToolContinuationIncomplete  = sessioncontract.ErrLiveToolContinuationIncomplete
)

// ErrSessionAudioResponseIncomplete is the CLI compatibility name for the
// reusable runtime's finite audio-response contract.
const ErrSessionAudioResponseIncomplete = sessioncontract.ErrLiveAudioResponseIncomplete

func joinSessionAudioOutputError(runErr error, path string, outputErr error) error {
	if outputErr == nil || errors.Is(runErr, outputErr) {
		return runErr
	}
	return errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
}

// SessionUnresolvedToolResultsError remains source-compatible for the legacy
// runtime while sharing the host-facing contract with the reusable runtime.
type SessionUnresolvedToolResultsError = sessionpublic.SessionUnresolvedToolResultsError

func newSessionUnresolvedToolResultsError(ids []string, statuses map[string]messages.SessionSendStatus) *SessionUnresolvedToolResultsError {
	return sessionpublic.NewSessionUnresolvedToolResultsError(ids, statuses)
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

// The concrete continuation errors live in services/session. These aliases
// keep the CLI's diagnostics and compatibility tests source-compatible while
// preventing a private host observer type from leaking into the runtime API.
type SessionImageContinuationError = sessioncontract.LiveImageContinuationError
type SessionToolContinuationError = sessioncontract.LiveToolContinuationError

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
