package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionMaxDurationReason is the stable terminal reason for a planned
// duration cutoff.
const SessionMaxDurationReason messages.TerminalReason = "max_duration"

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

// SessionDurationTimer is the timer contract owned by the session duration
// controller. The small interface gives deterministic tests a clock seam while
// ensuring the real timer is stopped on every termination path.
type SessionDurationTimer interface {
	C() <-chan time.Time
	Stop() bool
}

// SessionDurationClock creates one timer for a positive session bound.
type SessionDurationClock interface {
	NewTimer(time.Duration) SessionDurationTimer
}

// RunSessionWithMaxDuration runs a session with an optional graceful duration
// bound. A zero duration disables the controller; a positive duration requests
// a session close and drains the accepted output before finalization.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, realSessionDurationClock{})
}

// RunSessionWithMaxDurationClock is the deterministic-clock seam for the
// duration path. Production callers should use RunSessionWithMaxDuration.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, durationClock SessionDurationClock) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}

	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	// Zero disables this controller. Preserve the runtime's existing safety
	// behavior for replay and injected sessions when the flag is omitted.
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	if durationClock == nil {
		durationClock = realSessionDurationClock{}
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, durationClock)
}

// RunSessionWithTextSeedAndMaxDuration preserves the explicit --prompt seed
// behavior while applying the duration admission boundary before the seed
// wrapper's audio sink. A zero duration delegates to the existing text-seed
// path so omitted-duration behavior remains unchanged.
func RunSessionWithTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !seed.Present {
		return RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
	}
	if maxDuration == 0 {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}

	opts.Prompt = seed.Value
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}

	wirePrompt := nextSessionTextWirePrompt()
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	admission := newSessionDurationAdmission()
	var inner messages.SessionInferencer
	if plan.inferencer != nil {
		// The seed substitution wrapper must sit INSIDE the admission
		// boundary: the duration runner connects through admittedInferencer,
		// so any wrapper composed outside it never observes the session and
		// the sentinel prompt would leak onto the live wire.
		inner = &sessionTextSeedInferencer{
			inner:      plan.inferencer,
			wirePrompt: wirePrompt,
			value:      seed.Value,
			audioOut:   output,
		}
	}
	admittedInferencer := &sessionDurationAdmissionInferencer{
		inner:     inner,
		admission: admission,
		closeDone: make(chan struct{}),
	}
	if inner != nil {
		plan.inferencer = admittedInferencer
	}
	err = runSessionDurationPlanWithAdmission(durationCtx, output, plan, maxDuration, realSessionDurationClock{}, admittedInferencer)
	return errors.Join(err, output.errorValue())
}

func runSessionDurationPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runSessionDurationPlanWithAdmission(ctx, out, plan, maxDuration, durationClock, nil)
}

func runSessionDurationPlanWithAdmission(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) (runErr error) {
	artifacts := sessionDurationArtifactsFromContext(ctx)
	finalizer := newSessionRuntimeFinalizer(plan)
	defer func() {
		// The common finalizer must complete browser/provider/capture teardown
		// before the duration sidecar is flushed and closed as the final bundle
		// stage. This keeps duration, image, and recording runs on one C0 order.
		runErr = finalizer.finish(ctx, out, runErr)
		runErr = errors.Join(runErr, finalizeSessionDurationArtifacts(artifacts))
	}()
	deviceBinding, err := PrepareRTCDeviceBindings(plan.rtcDeviceRequest)
	if err != nil {
		return err
	}
	if deviceBinding != nil {
		plan.loop.rtcDeviceBinding = deviceBinding
		finalizer.setDeviceBinding(deviceBinding)
	}
	if plan.announce != "" {
		if _, err := fmt.Fprintln(out, plan.announce); err != nil {
			return wrapSessionRuntimeError(plan, err)
		}
	}

	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	plan.configureLoopObserver(&plan.loop)
	if plan.inferencer != nil {
		runErr = runAgentLoopSessionWithDurationAdmissionClock(ctx, loopOut, plan.inferencer, plan.loop, maxDuration, durationClock, admittedInferencer)
	}

	if runErr != nil {
		runErr = wrapSessionRuntimeError(plan, wrapSessionPhaseError("run session loop", runErr))
	}
	return runErr
}
