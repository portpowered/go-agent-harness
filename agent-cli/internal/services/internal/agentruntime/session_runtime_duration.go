package agentruntime

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// runSessionPlanWithDuration runs a planned session. A positive bound prepares
// the service-owned duration artifacts and hands the loop to the
// sessionduration service. wrap applies after live-evidence setup so a
// recording capture that replaces the provider inferencer still carries it.
func runSessionPlanWithDuration(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, clock sessionduration.TimerScheduler, wrap func(messages.SessionInferencer) messages.SessionInferencer) error {
	if maxDuration > 0 {
		clock, err := sessionPlanDurationClock(plan, clock)
		if err != nil {
			return err
		}
		durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
		if err != nil {
			return err
		}
		ctx = durationCtx
		plan.loop.durationBound, plan.loop.durationClock = maxDuration, clock
	}
	return plan.withLiveEvidence(ctx, func(runCtx context.Context, prepared sessionRuntimePlan) error {
		if wrap != nil && prepared.inferencer != nil {
			prepared.inferencer = wrap(prepared.inferencer)
		}
		return prepared.runPrepared(runCtx, out)
	})
}

// sessionPlanDurationClock keeps a bounded run on the plan's shared timing
// domain unless a deterministic clock is injected.
func sessionPlanDurationClock(plan sessionRuntimePlan, requested sessionduration.TimerScheduler) (sessionduration.TimerScheduler, error) {
	if requested != nil {
		return requested, nil
	}
	if plan.loop.audioService == nil {
		return nil, errors.New("audio service is required for session duration timing")
	}
	return plan.loop.audioService.NewClock(plan.clockSource)
}

// newFinalizer adapts the plan's owned resources to the service-owned ordered
// finalizer: capabilities, provider session, device binding, runtime, capture.
func (p sessionRuntimePlan) newFinalizer(reporter sessionterminal.Reporter) sessionduration.Finalizer {
	ports := sessionduration.FinalizationPorts{CloseSession: p.closeSession}
	if p.capabilityCoordinator != nil {
		ports.CloseCapabilities = p.capabilityCoordinator.Close
	}
	if p.rtcRuntime != nil {
		ports.CloseRuntime = p.rtcRuntime.Close
	}
	if p.flushCapture != nil {
		ports.FlushCapture = func() error { return wrapSessionRuntimeError(p, p.flushCapture()) }
	}
	if p.finalize != nil {
		ports.Finalize = func(ctx context.Context, out io.Writer) error {
			return wrapSessionRuntimeError(p, p.finalize(sessionterminalwire.WithReporter(ctx, reporter), out))
		}
	}
	return durationwire.NewService().NewFinalizer(ports)
}

// finishDurationArtifacts closes a bounded run's service-owned artifacts after
// the common finalizer, as the final bundle stage.
func (p sessionRuntimePlan) finishDurationArtifacts(ctx context.Context, reporter sessionterminal.Reporter, runErr error) error {
	if p.loop.durationBound <= 0 {
		return runErr
	}
	service := durationwire.NewService()
	artifacts := service.ArtifactsFromContext(ctx)
	artifactErr := service.FinalizeArtifacts(artifacts)
	reporter.RecordArtifactFinalization(artifacts != nil, artifactErr)
	return errors.Join(runErr, artifactErr)
}

// durationCompletionPublisher records a bounded run's completion facts in the
// live evidence bundle.
func (p sessionRuntimePlan) durationCompletionPublisher(ctx context.Context, observer sessiontrace.Observer) func(bool, error) error {
	if p.liveEvidence == nil || observer == nil || p.loop.durationBound <= 0 {
		return nil
	}
	return func(expired bool, completionErr error) error {
		state := observer.State()
		return p.liveEvidence.SetCompletion(context.WithoutCancel(ctx), runtimerecording.LiveCompletion{
			RunError: completionErr, DurationExpired: expired, SawSessionOpen: state.SawSessionOpen,
			TurnsCompleted: state.TurnsCompleted, OutputObserved: state.AssistantOutputObserved,
		})
	}
}
