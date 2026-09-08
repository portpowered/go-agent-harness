package agentruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	sessionpublic "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessioncontract "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
