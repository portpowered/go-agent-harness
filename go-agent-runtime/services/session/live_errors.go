package session

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrLiveNotStarted is returned by Wait when a handle has not been started.
	// Calling Wait before Start is a programming error and never waits forever.
	ErrLiveNotStarted = errors.New("live session has not been started")
	// ErrLiveClosed is returned when a caller attempts to start a handle after
	// it has been closed.
	ErrLiveClosed = errors.New("live session is closed")
	// ErrLiveDurationExceeded identifies a live session that reached its
	// explicit MaxDuration policy. The policy is enforced by the injected
	// scheduler, so deterministic hosts can advance it without wall time.
	ErrLiveDurationExceeded = errors.New("live session exceeded maximum duration")
	// ErrLiveSessionUpdatedTimeout identifies a live session that did not
	// receive the required SESSION.UPDATED acknowledgement before the
	// configured scheduler deadline.
	ErrLiveSessionUpdatedTimeout = errors.New("live session timed out awaiting session.updated")
	// ErrLiveFirstTurnTimeout identifies a live session whose first admitted
	// turn did not produce a provider response before the configured deadline.
	ErrLiveFirstTurnTimeout = errors.New("live session timed out awaiting first turn")
	// ErrLiveRateLimitRetryExhausted identifies a live session that received a
	// second rate-limit terminal after consuming its configured retry budget.
	ErrLiveRateLimitRetryExhausted = errors.New("live session exhausted rate-limit retry budget")
	// ErrLiveToolExecutionTimeout identifies a tool invocation that exceeded
	// the session's explicit tool execution budget.
	ErrLiveToolExecutionTimeout = errors.New("live tool execution timed out")
	// ErrLiveMediaUnavailable identifies a provider session that does not
	// expose the PCM endpoints required by an admitted media direction. The
	// sentinel is public so hosts can retain the cause when a device worker
	// reports the failure through its own typed wrapper.
	ErrLiveMediaUnavailable = errors.New("live provider does not expose PCM media")
	// ErrLiveSilentProviderEmptyResponse identifies an assistant response that
	// reached a partial-output terminal boundary without observable output.
	ErrLiveSilentProviderEmptyResponse = errors.New("silent provider returned an empty response")
	// ErrLiveSilentProviderTimeout identifies a response that stopped emitting
	// provider events before its participant-owned watchdog expired.
	ErrLiveSilentProviderTimeout = errors.New("silent provider response timed out")
	// ErrLiveSchedulerUnavailable means a request selected a scheduler-backed
	// timing policy but the service was constructed without the matching
	// application-owned scheduler.
	ErrLiveSchedulerUnavailable = errors.New("live session scheduler is required for timing policy")
	// ErrLiveImageContinuationIncomplete identifies a read_image result that
	// reached the provider but did not receive a completed model continuation.
	// The sentinel is part of the runtime contract so hosts can preserve the
	// actionable failure without importing the CLI's private observer.
	ErrLiveImageContinuationIncomplete = errors.New("session ended before the image tool continuation")
	// ErrLiveToolContinuationIncomplete identifies an ordinary tool result that
	// reached the provider but did not receive a completed model continuation.
	ErrLiveToolContinuationIncomplete = errors.New("session ended before the tool continuation")
	// ErrLiveScheduledAudioIncomplete identifies a finite scheduled-audio
	// invocation that ended before every admitted source received a terminal
	// response disposition. The runtime keeps this cause separate from a
	// provider, cancellation, or recording error so hosts can join all of the
	// evidence while still classifying the schedule failure.
	ErrLiveScheduledAudioIncomplete = errors.New("scheduled audio session ended before all turns completed")
)

// LiveImageContinuationError carries the read_image call IDs whose result was
// accepted but whose post-tool model response did not complete with observable
// output. Provider fields contain bounded terminal context from the provider
// neutral stream contract. Callers may use errors.Is with
// ErrLiveImageContinuationIncomplete and errors.As with this type.
type LiveImageContinuationError struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

func (e *LiveImageContinuationError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrLiveImageContinuationIncomplete.Error()
	}
	return fmt.Sprintf("image tool continuation was not completed for %d call(s): %s", len(e.CallIDs), formatLiveContinuationFailureIDs(e.CallIDs, e.ProviderStatuses, e.ProviderCodes, e.ProviderDetails))
}

func (e *LiveImageContinuationError) Unwrap() error {
	return ErrLiveImageContinuationIncomplete
}

// LiveToolContinuationError carries ordinary tool call IDs whose accepted
// result still lacks a terminal model continuation.
type LiveToolContinuationError struct {
	CallIDs          []string
	ProviderStatuses map[string]string
	ProviderCodes    map[string]string
	ProviderDetails  map[string]string
}

func (e *LiveToolContinuationError) Error() string {
	if e == nil || len(e.CallIDs) == 0 {
		return ErrLiveToolContinuationIncomplete.Error()
	}
	return fmt.Sprintf("tool continuation was not completed for %d call(s): %s", len(e.CallIDs), formatLiveContinuationFailureIDs(e.CallIDs, e.ProviderStatuses, e.ProviderCodes, e.ProviderDetails))
}

func (e *LiveToolContinuationError) Unwrap() error {
	return ErrLiveToolContinuationIncomplete
}

func formatLiveContinuationFailureIDs(ids []string, statuses, codes, details map[string]string) string {
	const annotationCapacity = 3 // status, code and detail
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	formatted := make([]string, 0, len(ordered))
	for _, id := range ordered {
		if strings.TrimSpace(id) == "" {
			continue
		}
		annotations := make([]string, 0, annotationCapacity)
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

// LiveScheduledAudioIncompleteError carries the deterministic schedule
// counters observed at a terminal boundary. Completed counts terminal
// response dispositions, Dispatched counts finite sources that crossed the
// runtime's media/control admission, and Scheduled is the caller's total
// source count. Provider fields retain bounded terminal context when a
// provider close caused the incomplete schedule.
type LiveScheduledAudioIncompleteError struct {
	Completed         int
	Dispatched        int
	Scheduled         int
	ProviderStatus    string
	ProviderErrorCode string
	ProviderDetails   string
}

func (e *LiveScheduledAudioIncompleteError) Error() string {
	if e == nil {
		return ErrLiveScheduledAudioIncomplete.Error()
	}
	message := fmt.Sprintf("%s: completed=%d dispatched=%d scheduled=%d", ErrLiveScheduledAudioIncomplete, e.Completed, e.Dispatched, e.Scheduled)
	const annotationCapacity = 3
	annotations := make([]string, 0, annotationCapacity)
	if status := strings.TrimSpace(e.ProviderStatus); status != "" {
		annotations = append(annotations, "status="+status)
	}
	if code := strings.TrimSpace(e.ProviderErrorCode); code != "" {
		annotations = append(annotations, "code="+code)
	}
	if detail := strings.TrimSpace(e.ProviderDetails); detail != "" {
		annotations = append(annotations, "detail="+detail)
	}
	if len(annotations) > 0 {
		message += " (" + strings.Join(annotations, "; ") + ")"
	}
	return message
}

func (*LiveScheduledAudioIncompleteError) Unwrap() error {
	return ErrLiveScheduledAudioIncomplete
}
