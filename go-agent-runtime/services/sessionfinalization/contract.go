// Package sessionfinalization owns the reusable terminal lifecycle contract
// shared by interactive and non-interactive session hosts.
package sessionfinalization

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// Cleanup is one best-effort terminal lifecycle action. The service invokes
// every admitted action in contract order even when an earlier action fails.
type Cleanup func() error

// Service is the public composition contract. It returns only public
// lifecycle interfaces; implementation state remains private to Wire.
type Service interface {
	NewFinalizer(FinalizerRequest) Finalizer
	NewTerminationBoundary(context.Context, TerminationRequest) TerminationBoundary
	SIGINTErrorOnly(error, ErrorTreeOptions) bool
	SIGINTCancellationOnly(error, SIGINTIntent, ErrorTreeOptions) bool
	SIGINTCleanForObserver(error, SIGINTIntent, *ObserverFailure, ErrorTreeOptions) bool
	SIGINTObserverFailureOnly(*ObserverFailure) bool
}

// FinalizerRequest contains host-owned lifecycle callbacks. The request is
// deliberately callback-based: the reusable service does not know about CLI
// devices, providers, recording implementations, or terminal state.
type FinalizerRequest struct {
	CloseCapabilities   Cleanup
	CloseSession        Cleanup
	CloseDevice         Cleanup
	CloseRuntime        Cleanup
	FlushCapture        Cleanup
	Finalize            func(context.Context, io.Writer) error
	FinalizeContext     func(context.Context) context.Context
	ReleaseCaptureClaim Cleanup

	// PhaseError and RuntimeError let a host retain its existing diagnostic
	// envelopes without importing that host's private implementation here.
	PhaseError   func(string, error) error
	RuntimeError func(error) error
}

// Finalizer owns post-loop cleanup and joins all cleanup failures with the
// initiating session result. Finish and Cleanup are safe to call repeatedly.
type Finalizer interface {
	Finish(context.Context, io.Writer, error) error
	Cleanup(context.Context, io.Writer) error
}

// DrainPolicy is the bounded policy supplied to the mandatory straggler
// drain. QuietPeriod allows already-accepted provider output to arrive;
// WallSafety caps teardown when the canonical clock is frozen.
type DrainPolicy struct {
	QuietPeriod time.Duration
	WallSafety  time.Duration
}

const (
	// DefaultStragglerDrainQuietPeriod is the deterministic provider quiet
	// period used by every terminal boundary.
	DefaultStragglerDrainQuietPeriod = 25 * time.Millisecond
	// DefaultStragglerDrainWallSafety bounds a drain on a frozen logical clock.
	DefaultStragglerDrainWallSafety = 250 * time.Millisecond
)

// TerminationRequest contains the loop-owned terminal callbacks. Waiting for
// stragglers is mandatory; omitting it is a configuration failure, not a
// buffered-only shutdown mode.
type TerminationRequest struct {
	QuiesceUpstream    Cleanup
	WaitForStragglers  func(DrainPolicy) error
	StopOwnedResources Cleanup
	FlushBuffered      Cleanup
}

// TerminationBoundary owns the one ordered terminal path for a session.
type TerminationBoundary interface {
	Terminate(error) error
}

// SIGINTIntent is the small host-owned operator intent seam used by the
// cancellation classifier. It intentionally does not expose signal handling.
type SIGINTIntent interface {
	SIGINTReceived() bool
}

// CancellationCauseProvider lets a host expose the causal error beneath a
// typed boundary whose ordinary Unwrap includes descriptive kind metadata.
type CancellationCauseProvider interface {
	CancellationCause() error
}

// ObserverFailure is the immutable observer evidence needed to distinguish a
// loop-owned cancellation from a provider-authored failure.
type ObserverFailure struct {
	TerminalReason string
	Provenance     string
}

const (
	TerminalReasonCancellation = "cancellation"
	TerminalProvenanceLoop     = "loop"
)

// ErrorTreeOptions supplies the leaf errors that are intentionally suppressed
// when SIGINT is the sole cause. An empty option set uses the public session
// defaults, which keeps external hosts independent of the CLI taxonomy.
type ErrorTreeOptions struct {
	CancellationErrors []error
}

// WithDefaults returns the public defaults plus host-specific legacy
// cancellation sentinels. It is a method so the root contract exposes only
// types and values; composition helpers remain in the Wire package.
func (ErrorTreeOptions) WithDefaults(extra ...error) ErrorTreeOptions {
	return ErrorTreeOptions{CancellationErrors: append([]error{
		context.Canceled,
		session.ErrLiveUserCancellation,
		session.ErrLiveAudioResponseIncomplete,
		session.ErrLiveImageContinuationIncomplete,
		session.ErrLiveScheduledAudioIncomplete,
		session.ErrLiveToolContinuationIncomplete,
		session.ErrLiveUnresolvedToolResults,
	}, extra...)}
}

// ContractError is a comparable sentinel type used for configuration and
// panic errors. It is exported so errors.Is remains stable for embedders.
type ContractError string

func (e ContractError) Error() string { return string(e) }

const (
	ErrInvalidStragglerDrainPolicy ContractError = "session straggler drain policy requires a positive quiet period"
	ErrMissingStragglerDrain       ContractError = "session termination boundary requires a straggler drain"
	ErrFinalizationPanic           ContractError = "session finalization panicked"
)
