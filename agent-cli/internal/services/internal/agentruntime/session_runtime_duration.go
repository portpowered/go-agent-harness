package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// SessionMaxDurationReason is the stable terminal reason for a planned
// duration cutoff.
const SessionMaxDurationReason messages.TerminalReason = "max_duration"

func (p sessionRuntimePlan) finalizationPorts() duration.FinalizationPorts {
	ports := duration.FinalizationPorts{
		CloseCapabilities: func() error {
			if p.capabilityCoordinator == nil {
				return nil
			}
			return p.capabilityCoordinator.Close()
		},
		CloseSession: p.closeSession,
		CloseRuntime: func() error {
			if p.rtcRuntime == nil {
				return nil
			}
			return p.rtcRuntime.Close()
		},
		FlushCapture: p.flushCapture,
		ReleaseCapture: func() error {
			if p.captureClaim == nil {
				return nil
			}
			return wrapSessionRuntimeError(p, p.captureClaim.release())
		},
	}
	if p.finalize != nil {
		ports.Finalize = func(ctx context.Context, out io.Writer) error {
			if reporter := p.loop.terminalReporter; reporter != nil {
				ctx = withSessionTerminalReporter(ctx, reporter)
			}
			return wrapSessionRuntimeError(p, p.finalize(ctx, out))
		}
	}
	return ports
}

// SessionDurationTimer is the timer contract owned by the session duration
// controller. The small interface gives deterministic tests a clock seam while
// ensuring the real timer is stopped on every termination path.
type SessionDurationTimer = platformclock.Timer

// SessionDurationClock creates one timer for a positive session bound.
type SessionDurationClock interface {
	NewTimer(time.Duration) SessionDurationTimer
}

func sessionDurationClockFromSource(source platformclock.Source) (SessionDurationClock, error) {
	timerSource, err := sessionTimerSource(source)
	if err != nil {
		return nil, err
	}
	return timerSource, nil
}

// RunSessionWithMaxDuration runs a session with an optional graceful duration
// bound. A zero duration disables the controller; a positive duration requests
// a session close and drains the accepted output before finalization.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	if maxDuration == 0 {
		return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, nil)
	}
	durationClock, err := sessionDurationClockFromSource(opts.Clock)
	if err != nil {
		return err
	}
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, durationClock)
}

// RunSessionWithMaxDurationClock is the deterministic-clock seam for the
// duration path. Production callers should use RunSessionWithMaxDuration.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, durationClock SessionDurationClock) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()

	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	// Zero disables this controller. Preserve the runtime's existing safety
	// behavior for replay and injected sessions when the flag is omitted.
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
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
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !seed.Present {
		return RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
	}
	if maxDuration == 0 {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}

	opts.Prompt = seed.Value
	opts.PromptProvided = true
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
	if err != nil {
		return err
	}

	wirePrompt := nextSessionTextWirePrompt()
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	admission := durationwire.NewService().NewEventAdmission()
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
		}
	}
	admittedInferencer := durationwire.NewService().NewAdmissionInferencer(inner, admission, make(chan struct{}))
	if inner != nil {
		plan.inferencer = admittedInferencer
	}
	durationClock, err := sessionDurationClockFromSource(opts.Clock)
	if err != nil {
		return err
	}
	err = runSessionDurationPlanWithAdmission(durationCtx, output, plan, maxDuration, durationClock, admittedInferencer)
	return errors.Join(err, output.errorValue())
}

func runSessionDurationPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runSessionDurationPlanWithAdmission(ctx, out, plan, maxDuration, durationClock, nil)
}

func effectiveSessionDurationClock(plan sessionRuntimePlan, requested SessionDurationClock) (SessionDurationClock, error) {
	// The legacy production callers pass realSessionDurationClock{} because
	// that was the only implementation before SessionRunOptions.Clock became
	// the shared timing dependency. Redirect that default through the plan's
	// source so deterministic sessions do not accidentally schedule on host
	// time. A non-default test clock remains authoritative.
	if requested == nil {
		return sessionDurationClockFromSource(plan.clockSource)
	}
	if _, isDefault := requested.(realSessionDurationClock); isDefault {
		return sessionDurationClockFromSource(plan.clockSource)
	}
	return requested, nil
}

func runSessionDurationPlanWithAdmission(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer duration.AdmissionInferencer) (runErr error) {
	var err error
	durationClock, err = effectiveSessionDurationClock(plan, durationClock)
	if err != nil {
		return err
	}
	artifacts := durationwire.NewService().ArtifactsFromContext(ctx)
	reporter := plan.loop.terminalReporter
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		plan.loop.terminalReporter = reporter
	}
	finalizer := durationwire.NewService().NewFinalizer(plan.finalizationPorts())
	defer func() {
		// The common finalizer must complete browser/provider/capture teardown
		// before the duration sidecar is flushed and closed as the final bundle
		// stage. This keeps duration, image, and recording runs on one C0 order.
		runErr = finalizer.Finish(ctx, out, runErr)
		artifactErr := durationwire.NewService().FinalizeArtifacts(artifacts)
		runErr = errors.Join(runErr, artifactErr)
		reporter.recordArtifactFinalization(artifacts != nil, artifactErr)
		if !sessionErrorHasIndependentFailure(runErr) && plan.replayCompletion != nil {
			plan.replayCompletion(reporter)
		}
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
	}()
	if err := prepareDurationSession(out, &plan, finalizer); err != nil {
		return err
	}
	return runDurationSessionLoop(ctx, out, plan, maxDuration, durationClock, admittedInferencer)
}

func prepareDurationSession(out io.Writer, plan *sessionRuntimePlan, finalizer duration.Finalizer) error {
	if plan.replayIntegrityWarning != "" {
		if _, err := fmt.Fprintln(out, plan.replayIntegrityWarning); err != nil {
			return err
		}
	}
	deviceBinding, err := PrepareRTCDeviceBindings(plan.rtcDeviceRequest)
	if err != nil {
		return err
	}
	if deviceBinding != nil {
		plan.loop.rtcDeviceBinding = deviceBinding
		finalizer.SetDeviceBinding(deviceBinding.Close)
	}
	// Best-effort, same as the non-duration run path: this disclosure write
	// must not pre-empt or masquerade as the session's own run/drain failure.
	writeFilesystemScopeAnnouncement(out, plan.filesystemPolicy)
	writeSessionToolAnnouncement(out, plan.toolDefinitionsForAnnouncement())
	announcement := plan.announce
	if plan.loop.BareLive {
		announcement, plan.loop.ListeningBanner = plan.bareLiveOutput(deviceBinding)
	}
	if announcement != "" {
		if _, err := fmt.Fprintln(out, announcement); err != nil {
			return wrapSessionRuntimeError(*plan, err)
		}
	}
	return nil
}

func runDurationSessionLoop(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer duration.AdmissionInferencer) error {
	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	plan.configureLoopObserver(&plan.loop)
	if plan.inferencer != nil {
		if plan.loop.terminalReporter != nil {
			plan.loop.terminalReporter.markRunStarted()
		}
		runErr := runAgentLoopSessionWithDurationAdmissionClock(ctx, loopOut, plan.inferencer, plan.loop, maxDuration, durationClock, admittedInferencer)
		if runErr != nil {
			return wrapSessionRuntimeError(plan, wrapSessionPhaseError("run session loop", runErr))
		}
		return nil
	}
	return nil
}
