// Package browserrunner owns provider-neutral browser conversation tracking.
//
// The package is deliberately limited to stream evidence, semantic navigation,
// and event-driven audio interruption. Host adapters supply the scenario model,
// browser implementation, and durable run recorder at the boundary.
package browserrunner

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ErrorKind is a comparable, immutable service error identity. Keeping the
// sentinels as typed constants preserves errors.Is matching without mutable
// package state in the service contract root.
type ErrorKind string

func (e ErrorKind) Error() string { return string(e) }

const (
	// ErrEvidence identifies the first malformed or out-of-order observation
	// accepted by a tracker.
	ErrEvidence ErrorKind = "browser runner evidence failed"
	// ErrTimeout identifies a step deadline that expired while its assistant
	// boundary was still pending.
	ErrTimeout ErrorKind = "browser runner timed out"
	// ErrInterruptionQueueFull identifies an interruption that could not be
	// admitted without waiting for the audio consumer.
	ErrInterruptionQueueFull ErrorKind = "browser runner interruption queue is full"
)

// StepBoundary is the small behavior projection needed by the tracker and
// interruption controller. The scenario contract remains owned by its host
// until the reviewed scenario package is available on the mainline.
type StepBoundary struct {
	ID         string
	Deadline   time.Duration
	Navigation *Navigation
	Interrupt  *InterruptionBoundary
	Cancel     *CancellationBoundary
}

// Navigation is a customer-owned page transition.
type Navigation struct {
	FromPageID string
	ToPageID   string
	URL        string
}

// InterruptionBoundary describes an overlap trigger observed at an in-flight
// browser invocation.
type InterruptionBoundary struct {
	Trigger  string
	ToolName string
}

// CancellationBoundary describes a spoken explicit stop request.
type CancellationBoundary struct {
	Reason string
}

// AudioInput is one bounded PCM input held for a semantic interruption or
// cancellation trigger. PCM is copied by constructors and queue admission.
type AudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	SourceSampleRate    int
	EndOfTurn           bool
}

// NavigationPort is the host-owned page transition seam. Generation is read
// before and after Navigate so the recorder can retain the causal boundary.
type NavigationPort interface {
	Navigate(context.Context, Navigation) error
	Generation() uint64
}

// NavigationObservation is the normalized evidence emitted for one customer
// navigation attempt.
type NavigationObservation struct {
	StepID             string
	Navigation         Navigation
	PreviousGeneration uint64
	Generation         uint64
	Error              error
}

// CancellationObservation joins the semantic overlap and explicit-cancel
// facts without treating a canceled invocation as completed.
type CancellationObservation struct {
	Interrupted             bool
	Requested               bool
	InvocationID            string
	FinalState              string
	Reason                  string
	InterruptedStepID       string
	CancelStepID            string
	OverlappingAudioSent    bool
	ExplicitCancelAudioSent bool
	LateEventsSuppressed    int
}

// RunRecorder is the host adapter for durable run evidence. Implementations
// must be safe for calls from the stream observer and broker-facing paths.
type RunRecorder interface {
	ObserveCustomerTurn(stepID, observed string) error
	ObserveAssistantTurn(stepID, observed string) error
	RecordNavigation(NavigationObservation) error
	RecordCancellation(CancellationObservation) error
	HasInvocation(stepID string) bool
}

// ErrorSink receives the first causal service error when a queue or recorder
// boundary cannot be completed. It is separate from RunRecorder so a host can
// preserve an error even when its run has already become immutable.
type ErrorSink interface {
	SetError(error)
}

// Service is the provider-neutral browserrunner composition contract. Hosts
// obtain isolated trackers, interruption controllers, and audio partitions
// from the dedicated Wire package; implementation state remains private.
type Service interface {
	NewEvidenceTracker(EvidenceTrackerConfig) EvidenceTracker
	NewInterruptionController(InterruptionControllerConfig) InterruptionController
	PartitionAudioInputs([]StepBoundary, []AudioInput) ([]AudioInput, map[string]AudioInput)
}

// CancelInvocationFunc is bound by the host after its broker wrapper exists.
type CancelInvocationFunc func(context.Context, string, string) error

// EvidenceTracker observes provider-neutral stream messages and coordinates
// customer/assistant boundaries with the host recorder.
type EvidenceTracker interface {
	Configure(context.Context, context.CancelFunc, NavigationPort)
	SetCancelInvocation(CancelInvocationFunc)
	NoteInFlight(stepID, invocationID string)
	InvocationStep(context.Context) (string, error)
	Observe(messages.StreamMessage)
	StopDeadline()
	LateEventCount() int
	CurrentStep() string
	SetError(error)
	Err() error
}

// EvidenceTrackerConfig wires one tracker to its immutable behavior projection
// and host recorder.
type EvidenceTrackerConfig struct {
	Steps []StepBoundary
	Run   RunRecorder
}

// InterruptionController converts a semantic in-flight invocation into one or
// two bounded audio inputs and records the associated cancellation evidence.
type InterruptionController interface {
	AudioInterruptions() <-chan AudioInput
	Active() bool
	ObserveInFlight(stepID, invocationID, toolName string)
	Close()
}

// InterruptionControllerConfig wires one controller to its behavior projection
// and held audio payloads. QueueCapacity is optional; a zero value uses the
// number of configured steps as the finite default.
type InterruptionControllerConfig struct {
	Steps         []StepBoundary
	Audio         map[string]AudioInput
	Run           RunRecorder
	ErrorSink     ErrorSink
	QueueCapacity int
}
