// Package providerliveness owns the provider-progress policy used by live
// session hosts. It deliberately exposes only provider-neutral observations,
// typed terminal facts, and an injected timer boundary.
package providerliveness

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultTimeout is the participant-owned provider progress budget.
	DefaultTimeout = 10 * time.Second

	// SilentProviderEmptyResponseClassification identifies an explicit
	// partial-output terminal with no admissible assistant output.
	SilentProviderEmptyResponseClassification = "silent_provider_empty_response"
	// SilentProviderTimeoutClassification identifies an outstanding response
	// that stopped producing provider progress before the watchdog expired.
	SilentProviderTimeoutClassification = "silent_provider_timeout"
)

type sentinel string

func (s sentinel) Error() string { return string(s) }

const (
	// ErrSilentProviderEmptyResponse is the stable empty-response sentinel.
	ErrSilentProviderEmptyResponse sentinel = "silent provider returned an empty response"
	// ErrSilentProviderTimeout is the stable watchdog sentinel.
	ErrSilentProviderTimeout sentinel = "silent provider response timed out"
)

// Timer is the small clock seam required by the liveness policy.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Clock creates timers for one service instance. Hosts can inject deterministic
// clocks without exposing any wall-clock or global state to the service.
type Clock interface {
	NewTimer(time.Duration) Timer
}

// ClockAdapter bridges a structurally compatible host clock without importing
// its timer package into the provider-liveness contract.
type ClockAdapter[T Timer] struct {
	Source interface{ NewTimer(time.Duration) T }
}

func (c ClockAdapter[T]) NewTimer(duration time.Duration) Timer {
	if c.Source == nil {
		return nil
	}
	return c.Source.NewTimer(duration)
}

// TimerFunc adapts a host timer without exposing a concrete clock package.
type TimerFunc struct {
	Channel  <-chan time.Time
	StopFunc func() bool
}

func (t TimerFunc) C() <-chan time.Time { return t.Channel }
func (t TimerFunc) Stop() bool {
	if t.StopFunc == nil {
		return false
	}
	return t.StopFunc()
}

// ClockFunc adapts a host timer factory at the composition boundary.
type ClockFunc func(time.Duration) Timer

func (f ClockFunc) NewTimer(duration time.Duration) Timer {
	if f == nil {
		return nil
	}
	return f(duration)
}

// TokenUsage is the bounded provider usage fact retained on a liveness error.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ReasoningTokens  int
}

// Error carries credential-free facts for a provider liveness failure.
type Error struct {
	Classification     string
	ResponseID         string
	TerminalReason     string
	TerminalProvenance string
	OutputState        string
	Usage              TokenUsage
}

func (e *Error) Error() string {
	if e == nil {
		return "provider liveness failure"
	}
	classification := strings.TrimSpace(e.Classification)
	if classification == "" {
		classification = SilentProviderEmptyResponseClassification
	}
	return fmt.Sprintf("%s: provider response produced no observable output", classification)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Classification == SilentProviderTimeoutClassification {
		return ErrSilentProviderTimeout
	}
	return ErrSilentProviderEmptyResponse
}

// EventKind identifies the provider boundary relevant to the watchdog.
type EventKind string

const (
	EventProgress       EventKind = "progress"
	EventMessageStart   EventKind = "MESSAGE.START"
	EventAudioStart     EventKind = "AUDIO.START"
	EventMessageEnd     EventKind = "MESSAGE.END"
	EventResponseCreate EventKind = "RESPONSE.CREATE"
)

// EventRole identifies the source role without importing a host message type.
type EventRole string

const EventRoleTool EventRole = "tool"

// Event is the provider-neutral observation used by dispatch and stream
// boundaries. Tool acknowledgements are deliberately excluded from progress.
type Event struct {
	Kind                EventKind
	Role                EventRole
	ResponseID          string
	ToolAcknowledgement bool
}

// ResponseEnd is the terminal observation supplied by a host after it has
// completed its own output and tool-lifecycle accounting.
type ResponseEnd struct {
	Event
	Status             string
	TerminalReason     string
	TerminalProvenance string
	OutputState        string
	Usage              TokenUsage
	OutputPresent      bool
	ToolObligation     bool
}

// Dependencies configure one isolated service instance.
type Dependencies struct {
	Clock     Clock
	Timeout   time.Duration
	OnFailure func(error)
}

// Service is the provider-progress contract. All policy state, timer
// generations, first-cause arbitration, local-tool suppression, and shutdown
// state remain private to its implementation.
type Service interface {
	ObserveProviderEvent(Event)
	ObserveProviderDispatch(Event)
	ObserveResponseEnd(ResponseEnd)
	Arm()
	Reset()
	Disarm()
	BeginLocalToolExecution()
	EndLocalToolExecution()
	SetClock(Clock)
	Failure() error
	Events() <-chan struct{}
	FailureChannel(context.Context) <-chan error
	Stop()
}
