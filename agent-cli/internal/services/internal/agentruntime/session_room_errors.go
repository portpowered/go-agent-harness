package agentruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type roomLifecycleWorkError struct {
	outstanding []string
}

func (e *roomLifecycleWorkError) Error() string {
	if e == nil || len(e.outstanding) == 0 {
		return "room lifecycle work did not complete"
	}
	return "room lifecycle work did not complete: " + strings.Join(e.outstanding, "; ")
}

func newRoomLifecycleWorkError(outstanding ...string) error {
	seen := make(map[string]struct{}, len(outstanding))
	ordered := make([]string, 0, len(outstanding))
	for _, item := range outstanding {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return &roomLifecycleWorkError{outstanding: ordered}
}

func roomLifecycleWorkLabel(participantID, phase string) string {
	if participantID == "" {
		return phase
	}
	return fmt.Sprintf("participant %q phase %s", participantID, phase)
}

func roomParticipantFailure(participantID string, err error, secrets []string) error {
	if err == nil {
		err = errors.New("unknown room participant failure")
	}
	return &roomSafeError{
		prefix:        fmt.Sprintf("room participant %q", participantID),
		participantID: participantID,
		cause:         err,
		secrets:       append([]string(nil), secrets...),
	}
}

// roomParticipantFailureReason returns the credential-free cause carried by a
// participant_failed room event. roomSafeError deliberately keeps the
// participant identity in its outer message for command/result diagnostics;
// the event already carries that identity separately, so publish only its
// sanitized local cause here.
func roomParticipantFailureReason(err error, terminationReason ParticipantTerminationReason, closeReason string, transportEnded bool, secrets []string) string {
	if cause := roomParticipantFailureCause(err, secrets); cause != "" {
		return cause
	}
	if closeReason = strings.TrimSpace(closeReason); closeReason != "" {
		if reason := strings.TrimSpace(sanitizeRoomError(errors.New(closeReason), secrets)); reason != "" {
			return reason
		}
	}
	if transportEnded {
		return "transport disconnected"
	}
	switch terminationReason {
	case ParticipantTerminationDisconnected:
		return "participant disconnected"
	case ParticipantTerminationError:
		return "participant failure"
	default:
		return "participant failure"
	}
}

func roomParticipantFailureCause(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	var safe *roomSafeError
	if errors.As(err, &safe) && safe != nil {
		secrets = append(append([]string(nil), secrets...), safe.secrets...)
		err = safe.cause
	}
	if err == nil {
		return ""
	}
	return strings.TrimSpace(sanitizeRoomError(err, secrets))
}

func roomFailureResult(err error, secrets []string) RoomResult {
	return RoomResult{
		TerminationReason: RoomTerminationFailed,
		Reason:            RoomTerminationFailed,
		Error:             sanitizeRoomError(err, secrets),
		Participants:      make(map[string]RoomParticipantResult),
	}
}

type roomSafeError struct {
	prefix        string
	participantID string
	cause         error
	secrets       []string
}

func (e *roomSafeError) Error() string {
	if e == nil {
		return "room failure"
	}
	if e.cause == nil {
		return e.prefix
	}
	return e.prefix + ": " + sanitizeRoomError(e.cause, e.secrets)
}

func (e *roomSafeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func sanitizeRoomError(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return redactRoomErrorText(value, "")
}

func withRoomEvidenceContext(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func redactRoomErrorText(value, secret string) string {
	if value == "" {
		return ""
	}
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, marker := range []string{
		"authorization: bearer ", "authorization=bearer ", "authorization: ", "authorization=",
		"x-api-key: ", "x-api-key=", "api-key: ", "api-key=", "api_key: ", "api_key=", "bearer ",
	} {
		value = redactRoomMarker(value, marker)
	}
	return value
}

func redactRoomMarker(value, marker string) string {
	searchFrom := 0
	for searchFrom < len(value) {
		start := strings.Index(strings.ToLower(value[searchFrom:]), marker)
		if start < 0 {
			return value
		}
		markerEnd := searchFrom + start + len(marker)
		if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
			searchFrom = markerEnd + len("[REDACTED]")
			continue
		}
		end := roomTokenEnd(value, markerEnd)
		value = value[:markerEnd] + "[REDACTED]" + value[end:]
		searchFrom = markerEnd + len("[REDACTED]")
	}
	return value
}

func roomTokenEnd(value string, start int) int {
	for end := start; end < len(value); end++ {
		switch value[end] {
		case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
			return end
		}
	}
	return len(value)
}

func secretsForPlan(plan *roomParticipantPlan) []string {
	if plan == nil || plan.secret == "" {
		return nil
	}
	return []string{plan.secret}
}
