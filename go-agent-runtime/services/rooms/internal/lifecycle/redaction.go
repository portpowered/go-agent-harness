package lifecycle

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
)

// redactedMarker replaces a participant credential in host-visible run
// errors, matching the room evidence redaction marker.
const redactedMarker = "[REDACTED]"

// redactedRunError presents a run error with participant credentials
// replaced. It keeps the original chain for errors.Is/As classification.
type redactedRunError struct {
	message string
	cause   error
}

func (e *redactedRunError) Error() string { return e.message }

func (e *redactedRunError) Unwrap() error { return e.cause }

// redactRun removes the room's participant credentials from every
// host-visible failure string. The secret set is the one room evidence
// redacts, resolved only when a failure string exists.
func redactRun(result rooms.RoomResult, runErr error, manifest rooms.Manifest, request rooms.RoomRunOptions) (rooms.RoomResult, error) {
	if runErr == nil && !resultHasErrorText(result) {
		return result, nil
	}
	secrets := planning.EvidenceSecrets(manifest, request)
	if len(secrets) == 0 {
		return result, runErr
	}
	result.Error = redactSecrets(result.Error, secrets)
	if len(result.Participants) > 0 {
		participants := make(map[string]rooms.RoomParticipantResult, len(result.Participants))
		for id, participant := range result.Participants {
			participant.Error = redactSecrets(participant.Error, secrets)
			participant.TerminalReason = redactSecrets(participant.TerminalReason, secrets)
			participants[id] = participant
		}
		result.Participants = participants
	}
	return result, redactError(runErr, secrets)
}

func resultHasErrorText(result rooms.RoomResult) bool {
	if result.Error != "" {
		return true
	}
	for _, participant := range result.Participants {
		if participant.Error != "" || participant.TerminalReason != "" {
			return true
		}
	}
	return false
}

func redactError(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	redacted := redactSecrets(message, secrets)
	if redacted == message {
		return err
	}
	return &redactedRunError{message: redacted, cause: err}
}

// redactSecrets replaces each secret; EvidenceSecrets orders longer values
// first so a secret containing another is never left partially visible.
func redactSecrets(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, redactedMarker)
		}
	}
	return value
}
