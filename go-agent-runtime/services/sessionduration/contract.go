// Package sessionduration defines the host-neutral terminal boundary for a
// bounded session. Provider observations, output projection, publication
// ports, and lifecycle error facts are explicit; host resources stay outside
// the contract.
package sessionduration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var (
	// ErrInvalidDuration identifies a negative duration before any runtime
	// effect is admitted.
	ErrInvalidDuration = errors.New("invalid session max duration")
	// ErrMaxDurationExceeded identifies the controller's bounded expiry.
	ErrMaxDurationExceeded = errors.New("session exceeded maximum duration")
	// ErrProviderEmptyResponse identifies a terminal response with no output.
	ErrProviderEmptyResponse = errors.New("silent provider returned an empty response")
	// ErrProviderLivenessTimeout identifies a response that made no progress
	// before the injected liveness deadline.
	ErrProviderLivenessTimeout = errors.New("silent provider response timed out")
	// ErrSchedulerUnavailable identifies a selected timing policy without an
	// application-owned scheduler.
	ErrSchedulerUnavailable = errors.New("session duration scheduler is required")
)

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

// ValidateDuration is the public pre-effect validation seam used by hosts
// that need to reject an invalid bound before constructing a session plan.
func ValidateDuration(duration time.Duration) error {
	if duration < 0 {
		return &InvalidDurationError{Duration: duration}
	}
	return nil
}

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

// LivenessOptions describes one response-progress watchdog.
type LivenessOptions struct {
	Enabled bool
	Timeout time.Duration
}

// LivenessError carries bounded, credential-free provider facts while
// retaining a stable errors.Is identity for the failure class.
type LivenessError struct {
	Classification string
	ResponseID     string
	Usage          messages.TokenUsage
	Cause          error
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
	if e == nil || e.Cause == nil {
		return ErrProviderEmptyResponse
	}
	return e.Cause
}

// RetryPolicy describes a bounded provider retry budget. Retry never sleeps;
// it returns a decision for the loop's injected scheduler.
type RetryPolicy struct {
	Enabled      bool
	MaxRetries   int
	DefaultDelay time.Duration
	MaxDelay     time.Duration
}

// Options creates one isolated controller. Context cancellation stops timing
// workers; no provider, filesystem, or device effect occurs here.
type Options struct {
	Context     context.Context
	Clock       platformclock.Scheduler
	MaxDuration time.Duration
	Liveness    LivenessOptions
	Retry       RetryPolicy
	Terminal    TerminalSource
	Publication Publication
	Artifacts   ArtifactLifecycle
	FirstCause  func(error)
}

// Admission is the controller's response-boundary decision. Rejected
// nonterminal messages are never forwarded to the loop or artifacts.
type Admission struct {
	Message      messages.StreamMessage
	Accepted     bool
	OutputState  messages.TerminalOutputState
	LivenessErr  error
	TerminalSeen bool
}

// RetryRequest contains a provider terminal candidate.
type RetryRequest struct{ Terminal *messages.MessageEndValue }

// RetryDecision contains only bounded eligibility data. The caller owns the
// actual wait and response.create dispatch.
type RetryDecision struct {
	Delay     time.Duration
	Eligible  bool
	Exhausted bool
}

// FinalizeRequest supplies ordered cleanup ports owned by the host.
type FinalizeRequest struct {
	Primary   error
	Drain     func(context.Context) error
	Close     func() error
	Binding   func() error
	Artifacts ArtifactLifecycle
}

// Result is the controller's terminal snapshot after cleanup.
type Result struct {
	OutputState     messages.TerminalOutputState
	TerminalWritten bool
	Expired         bool
}

// State owns one bounded-session observation and terminal-admission lifecycle.
type State interface {
	Observe(messages.StreamMessage)
	OutputState() messages.TerminalOutputState
	Admit(bool, messages.StreamMessage) (messages.StreamMessage, bool)
	PublishProviderTerminal(Publication) error
	PublishMaxDuration(Publication, messages.TerminalOutputState) error
	Written() bool
}

// Controller owns one session's mutable shutdown, admission, liveness, retry,
// and finalization state.
type Controller interface {
	Observe(messages.StreamMessage) Admission
	Errors() <-chan error
	Expire() error
	Retry(RetryRequest) RetryDecision
	OutputState() messages.TerminalOutputState
	TerminalWritten() bool
	Finalize(context.Context, FinalizeRequest) (Result, error)
}

// LifecycleFailures contains independent shutdown causes. The service joins
// each non-nil cause while retaining errors.Is/As identity.
type LifecycleFailures struct {
	Runtime error
	Close   error
	Binding error
}

// Service owns terminal precedence, output projection, publication ordering,
// terminal synthesis, and normalized error composition.
type Service interface {
	Begin(Options) (Controller, error)
	NewState(TerminalSource) State
	PublishMaxDuration(Publication, messages.TerminalOutputState) error
	LifecycleError(LifecycleFailures) error
	TransportError(error) error
}
