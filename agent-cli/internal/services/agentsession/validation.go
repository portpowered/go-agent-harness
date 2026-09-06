package agentsession

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidSessionMaxDuration identifies a negative --max-duration value.
var ErrInvalidSessionMaxDuration = errors.New("invalid session max duration")

// SessionMaxDurationError describes a duration that cannot be used as a
// session bound. It is returned before runtime planning or session startup.
type SessionMaxDurationError struct {
	Duration time.Duration
}

// InvalidSessionDurationError is retained as a descriptive alias for callers
// that use the validation error by its general duration name.
type InvalidSessionDurationError = SessionMaxDurationError

// Error returns an actionable validation message for the CLI.
func (e *SessionMaxDurationError) Error() string {
	if e == nil {
		return ErrInvalidSessionMaxDuration.Error()
	}
	return fmt.Sprintf("--max-duration must be non-negative, got %s", e.Duration)
}

// Unwrap preserves a stable errors.Is identity for duration validation.
func (e *SessionMaxDurationError) Unwrap() error {
	return ErrInvalidSessionMaxDuration
}

// ValidateSessionMaxDuration validates the optional session duration before
// any provider, session, or output resource is planned.
func ValidateSessionMaxDuration(duration time.Duration) error {
	if duration < 0 {
		return &SessionMaxDurationError{Duration: duration}
	}
	return nil
}

var ErrSessionAudioInTurnBargeRequiresSequence = errors.New("--audio-in-turn-barge requires at least two --audio-in-turn values")

// SessionAudioInTurnBargeError reports an invalid --audio-in-turn-barge
// cardinality before session setup or provider connection.
type SessionAudioInTurnBargeError struct {
	TurnCount int
}

func (e *SessionAudioInTurnBargeError) Error() string {
	if e == nil {
		return ErrSessionAudioInTurnBargeRequiresSequence.Error()
	}
	return fmt.Sprintf("%s; got %d", ErrSessionAudioInTurnBargeRequiresSequence, e.TurnCount)
}

func (e *SessionAudioInTurnBargeError) Unwrap() error {
	return ErrSessionAudioInTurnBargeRequiresSequence
}

// ValidateSessionAudioInTurnBarge validates the explicit scheduled-turn
// policy before any provider or capability setup. The ordinary one-turn and
// multi-turn paths remain valid when the opt-in is omitted.
func ValidateSessionAudioInTurnBarge(enabled bool, turnCount int) error {
	if !enabled || turnCount >= 2 {
		return nil
	}
	if turnCount < 0 {
		turnCount = 0
	}
	return &SessionAudioInTurnBargeError{TurnCount: turnCount}
}

// ValidateOpenAIRealtimeReasoningEffort validates the documented Realtime
// reasoning budgets. Empty preserves the provider default.
func ValidateOpenAIRealtimeReasoningEffort(effort string) error {
	switch strings.TrimSpace(effort) {
	case "", "minimal", "low", "medium", "high", "xhigh":
		return nil
	default:
		return fmt.Errorf("--reasoning-effort must be one of minimal, low, medium, high, or xhigh; got %q", effort)
	}
}
