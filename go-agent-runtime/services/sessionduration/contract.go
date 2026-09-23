// Package sessionduration defines the host-neutral terminal boundary for a
// bounded session. Provider observations, output projection, publication
// ports, and lifecycle error facts are explicit; host resources stay outside
// the contract.
package sessionduration

import (
	"context"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type sessionDurationError string

func (e sessionDurationError) Error() string { return string(e) }

const (
	ErrInvalidDuration                  sessionDurationError = "invalid session max duration"
	ErrMaxDurationExceeded              sessionDurationError = "session exceeded maximum duration"
	ErrProviderEmptyResponse            sessionDurationError = "silent provider returned an empty response"
	ErrProviderLivenessTimeout          sessionDurationError = "silent provider response timed out"
	ErrAssistantResponseIncomplete      sessionDurationError = "audio session ended before the final assistant response"
	ErrScheduledAudioIncomplete         sessionDurationError = "scheduled audio session ended before all turns completed"
	ErrSchedulerUnavailable             sessionDurationError = "session duration scheduler is required"
	ErrFinalizationPanic                sessionDurationError = "session finalization panicked"
	ErrFirstResponseTimeout             sessionDurationError = "session first response timed out"
	ErrRateLimitRetryExhausted          sessionDurationError = "session duration exhausted rate-limit retry budget"
	LivenessClassificationEmptyResponse                      = "silent_provider_empty_response"
	LivenessClassificationTimeout                            = "silent_provider_timeout"
	// MaxDurationReason is the stable terminal reason published when the
	// duration controller ends a run at its configured bound.
	MaxDurationReason messages.TerminalReason = "max_duration"
)

// Timer is the timer contract shared by duration and liveness controllers.
// It is an alias so host clocks can be passed without an adapter.
type Timer = platformclock.Timer

// TimerScheduler is the minimal clock contract required by the controller.
// Context scheduling remains a host concern; the controller only owns timers.
type TimerScheduler interface {
	NewTimer(time.Duration) Timer
}

// InvalidDurationError preserves the stable validation identity and the
// offending value without importing a CLI validation package.
type InvalidDurationError struct{ Duration time.Duration }

func (e *InvalidDurationError) Error() string {
	if e == nil {
		return ErrInvalidDuration.Error()
	}
	return fmt.Sprintf("--max-duration must be non-negative, got %s", e.Duration)
}

func (e *InvalidDurationError) Unwrap() error { return ErrInvalidDuration }

// TerminalSource supplies the provider observation facts needed to distinguish
// a provider-authored close from a loop shutdown request.
type TerminalSource struct {
	Message func() (messages.StreamMessage, bool)
	Matches func(messages.StreamMessage) bool
}

// MessageWriter is an injected publication port. The service never opens or
// owns the underlying stream or artifact resource.
type MessageWriter func(messages.StreamMessage) error

// ArtifactWriter is the host-neutral artifact admission port.
type ArtifactWriter interface {
	Accept(messages.StreamMessage) error
}

// Publication describes the ordered artifact and output effects for one
// terminal message.
type Publication struct {
	Artifacts ArtifactWriter
	Write     MessageWriter
}

// ArtifactLifecycle is the bounded lifecycle needed during ordered
// finalization. Implementations remain host-owned; the controller only calls
// the explicit port methods.
type ArtifactLifecycle interface {
	ArtifactWriter
	Flush() error
	Close() error
}

// EventAdmission is the opaque admission boundary assembled by the service.
// Hosts pass it back to NewAdmissionInferencer without owning its state.
type EventAdmission interface{}

// AdmissionInferencer is the provider bridge that applies the event
// admission boundary before the session loop sees provider messages.
type AdmissionInferencer interface {
	messages.SessionInferencer
	CloseError() error
	RuntimeError() error
	WaitForClose()
	ProviderTerminalMessage() (messages.StreamMessage, bool)
	IsProviderTerminalMessage(messages.StreamMessage) bool
	CloseAdmission()
}

// AdmissionSession is the wrapped provider session exposed for host seams
// that need optional complete-message capabilities.
type AdmissionSession interface {
	messages.Session
	// SessionAdmissionClosed and SessionAdmissionAllows preserve an optional
	// outer transport admission boundary through duration event wrapping.
	SessionAdmissionClosed() bool
	SessionAdmissionAllows(messages.StreamMessage) bool
	SessionAdmissionAllowsCompleteMessage(messages.Message) bool
	SendMessage(context.Context, messages.Message) bool
	SendMessageWithoutResponse(context.Context, messages.Message) bool
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// PlaybackDrainer is an optional session capability used during bounded
// finalization. Admission wrappers preserve it so accepted device playback
// can drain before the binding closes.
type PlaybackDrainer interface {
	DrainPlayback(context.Context) error
}

// LivenessOptions describes one response-progress watchdog.
type LivenessOptions struct {
	Enabled bool
	Timeout time.Duration
	// RequireFirstResponse starts a bounded first-response timer after the
	// session opens. FirstResponseTimeout is normalized by the service.
	RequireFirstResponse bool
	FirstResponseTimeout time.Duration
}

// LivenessError carries bounded, credential-free provider facts while
// retaining a stable errors.Is identity for the failure class.
type LivenessError struct {
	Classification     string
	ResponseID         string
	FailingEvent       messages.StreamMessageType
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Usage              messages.TokenUsage
	Cause              error
}

func (e *LivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	if e.Classification == "" {
		return "session liveness failure"
	}
	return fmt.Sprintf("%s: provider response produced no observable output", e.Classification)
}

func (e *LivenessError) Unwrap() error {
	if e == nil {
		return ErrProviderEmptyResponse
	}
	if e.Cause != nil {
		return e.Cause
	}
	if e.Classification == LivenessClassificationTimeout {
		return ErrProviderLivenessTimeout
	}
	return ErrProviderEmptyResponse
}
