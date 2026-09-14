package agentsession

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// ErrInvalidSessionMaxDuration identifies a negative --max-duration value.
var ErrInvalidSessionMaxDuration = sessionduration.ErrInvalidDuration

// SessionMaxDurationError describes a duration that cannot be used as a
// session bound. It is returned before runtime planning or session startup.
type SessionMaxDurationError = sessionduration.InvalidDurationError

// InvalidSessionDurationError is retained as a descriptive alias for callers
// that use the validation error by its general duration name.
type InvalidSessionDurationError = SessionMaxDurationError

// ValidateSessionMaxDuration validates the optional session duration before
// any provider, session, or output resource is planned.
func ValidateSessionMaxDuration(duration time.Duration) error {
	return sessionduration.ValidateDuration(duration)
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
