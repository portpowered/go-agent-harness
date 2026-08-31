package services

import (
	"errors"
	"fmt"
	"strings"
)

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
	return redactSelfPlayError(value, "")
}

func secretsForPlan(plan *roomParticipantPlan) []string {
	if plan == nil || plan.secret == "" {
		return nil
	}
	return []string{plan.secret}
}
