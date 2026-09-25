package events

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

// RedactedMarker replaces every credential occurrence, matching the marker
// room evidence writes.
const RedactedMarker = "[REDACTED]"

// Redactor replaces resolved credential values in projected text.
type Redactor struct{ secrets []string }

// NewRedactor keeps the non-empty secrets in the supplied order; the planner
// orders them longest first so a secret containing another is never left
// partially visible.
func NewRedactor(secrets []string) Redactor {
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			kept = append(kept, secret)
		}
	}
	return Redactor{secrets: kept}
}

// Redact returns value with every secret replaced by RedactedMarker.
func (r Redactor) Redact(value string) string {
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, RedactedMarker)
	}
	return value
}

// RedactError presents err with every secret replaced while keeping the
// original chain for errors.Is/As classification.
func (r Redactor) RedactError(err error) error {
	if err == nil {
		return nil
	}
	message := r.Redact(err.Error())
	if message == err.Error() {
		return err
	}
	return &redactedError{message: message, cause: err}
}

// RedactResult removes secrets from every failure string of a room result.
func (r Redactor) RedactResult(result rooms.RoomResult) rooms.RoomResult {
	result.Error = r.Redact(result.Error)
	if result.Participants == nil {
		return result
	}
	participants := make(map[string]rooms.RoomParticipantResult, len(result.Participants))
	for id, participant := range result.Participants {
		participant.Error = r.Redact(participant.Error)
		participant.TerminalReason = r.Redact(participant.TerminalReason)
		participants[id] = participant
	}
	result.Participants = participants
	return result
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }

func (e *redactedError) Unwrap() error { return e.cause }
