package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimeDuration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"time"
)

const SessionMaxDurationReason messages.TerminalReason = "max_duration"

var ErrInvalidSessionMaxDuration = sessioncontract.ErrInvalidSessionMaxDuration

type SessionMaxDurationError = sessioncontract.SessionMaxDurationError
type InvalidSessionDurationError = sessioncontract.InvalidSessionDurationError
type SessionDurationTimer = platformclock.Timer
type SessionDurationClock = runtimeDuration.Clock
type SessionDurationArtifactLifecycle = runtimeDuration.ArtifactLifecycle
type SessionDurationArtifactPaths = runtimeDuration.ArtifactPaths
type sessionDurationTerminalRecorder = runtimeDuration.TerminalRecorder
type durationMessage = messages.StreamMessage
type sessionDurationArtifactsContextKey struct{}
type sessionDurationArtifactPathsContextKey struct{}
type sessionDurationTerminalRecorderContextKey struct{}

func sessionDurationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func WithSessionDurationArtifacts(ctx context.Context, artifacts SessionDurationArtifactLifecycle) context.Context {
	return context.WithValue(sessionDurationContext(ctx), sessionDurationArtifactsContextKey{}, artifacts)
}
func sessionDurationArtifactsFromContext(ctx context.Context) SessionDurationArtifactLifecycle {
	value, _ := ctx.Value(sessionDurationArtifactsContextKey{}).(SessionDurationArtifactLifecycle)
	return value
}
func WithSessionDurationArtifactPaths(ctx context.Context, paths SessionDurationArtifactPaths) context.Context {
	return context.WithValue(sessionDurationContext(ctx), sessionDurationArtifactPathsContextKey{}, &paths)
}
func sessionDurationArtifactPathsForRequest(ctx context.Context) *SessionDurationArtifactPaths {
	paths, _ := ctx.Value(sessionDurationArtifactPathsContextKey{}).(*SessionDurationArtifactPaths)
	return paths
}
func prepareSessionDurationArtifacts(ctx context.Context) (context.Context, error) { return ctx, nil }
func withSessionDurationTerminalRecorder(ctx context.Context, recorder runtimeDuration.TerminalRecorder) context.Context {
	return context.WithValue(sessionDurationContext(ctx), sessionDurationTerminalRecorderContextKey{}, recorder)
}
func sessionDurationTerminalRecorderFromContext(ctx context.Context) runtimeDuration.TerminalRecorder {
	value, _ := ctx.Value(sessionDurationTerminalRecorderContextKey{}).(runtimeDuration.TerminalRecorder)
	return value
}

func newSessionDurationAdmission() *struct{} { return &struct{}{} }

type durationAdmission struct {
	inner     messages.SessionInferencer
	admission *struct{}
	closeDone chan struct{}
}
type sessionDurationAdmissionInferencer = durationAdmission

func (i *durationAdmission) ConnectSession(ctx context.Context) (messages.Session, error) {
	return i.inner.ConnectSession(ctx)
}
func (*durationAdmission) providerTerminalMessage() (m durationMessage, ok bool) { return }
func (*durationAdmission) isProviderTerminalMessage(durationMessage) bool        { return false }

func recordingTerminalSummaryFromMessage(msg durationMessage) (*transcript.RecordingTerminalSummary, bool, error) {
	return (runtimeDuration.TerminalSummaryDecoder{}).FromMessage(msg)
}

func writeDurationSessionReplayMessage(out interface{ Write([]byte) (int, error) }, msg messages.StreamMessage, artifacts SessionDurationArtifactLifecycle) error {
	if artifacts != nil {
		if err := artifacts.Accept(msg); err != nil {
			return wrapSessionPhaseError("write duration artifacts", err)
		}
	}
	return writeSessionReplayMessage(out, msg)
}

func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	if maxDuration == 0 {
		return RunSessionWithMaxDurationClock(ctx, out, opts, 0, nil)
	}
	clock, err := sessionTimerSource(opts.Clock)
	if err != nil {
		return err
	}
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, clock)
}
func runSessionDurationEntry(opts SessionRunOptions, maxDuration time.Duration, run func(SessionRunOptions, sessionRuntimePlan) error) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
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
	return run(opts, plan)
}
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, clock SessionDurationClock) error {
	return runSessionDurationEntry(opts, maxDuration, func(opts SessionRunOptions, plan sessionRuntimePlan) error {
		if maxDuration == 0 {
			return plan.run(ctx, out)
		}
		if clock == nil {
			clock = realSessionDurationClock{}
		}
		return runSessionDurationPlan(ctx, out, plan, maxDuration, clock)
	})
}
func RunSessionWithTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed) error {
	if !seed.Present {
		return RunSessionWithMaxDuration(ctx, out, opts, maxDuration)
	}
	if maxDuration == 0 {
		return RunSessionWithTextSeed(ctx, out, opts, seed)
	}
	opts.Prompt, opts.PromptProvided = seed.Value, true
	return runSessionDurationEntry(opts, maxDuration, func(opts SessionRunOptions, plan sessionRuntimePlan) error {
		wirePrompt := nextSessionTextWirePrompt()
		plan.loop.Prompt = wirePrompt
		output := &sessionTextOutput{writer: out}
		if plan.inferencer != nil {
			plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed.Value}
		}
		clock, err := sessionTimerSource(opts.Clock)
		if err != nil {
			return err
		}
		return errors.Join(runSessionDurationPlan(ctx, output, plan, maxDuration, clock), output.errorValue())
	})
}
func runSessionDurationPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runSessionDurationPlanWithAdmission(ctx, out, plan, maxDuration, durationClock, nil)
}
func effectiveSessionDurationClock(plan sessionRuntimePlan, requested SessionDurationClock) (SessionDurationClock, error) {
	switch requested.(type) {
	case nil, realSessionDurationClock:
		return sessionTimerSource(plan.clockSource)
	default:
		return requested, nil
	}
}
func runSessionDurationPlanWithAdmission(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, requested SessionDurationClock, _ *sessionDurationAdmissionInferencer) (runErr error) {
	durationClock, err := effectiveSessionDurationClock(plan, requested)
	if err != nil {
		return err
	}
	reporter := plan.loop.terminalReporter
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		plan.loop.terminalReporter = reporter
	}
	finalizer := newSessionRuntimeFinalizer(plan)
	defer func() {
		runErr = finalizer.finish(ctx, out, runErr)
		if !sessionErrorHasIndependentFailure(runErr) && plan.replayCompletion != nil {
			plan.replayCompletion(reporter)
		}
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
	}()
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
		finalizer.setDeviceBinding(deviceBinding)
	}
	writeFilesystemScopeAnnouncement(out, plan.filesystemPolicy)
	writeSessionToolAnnouncement(out, plan.toolDefinitionsForAnnouncement())
	announcement := plan.announce
	if plan.loop.BareLive {
		announcement, plan.loop.ListeningBanner = plan.bareLiveOutput(deviceBinding)
	}
	if announcement != "" {
		if _, err := fmt.Fprintln(out, announcement); err != nil {
			return wrapSessionRuntimeError(plan, err)
		}
	}
	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	plan.configureLoopObserver(&plan.loop)
	if plan.inferencer != nil {
		reporter.markRunStarted()
		runErr = runAgentLoopSessionWithDurationClock(ctx, loopOut, plan.inferencer, plan.loop, maxDuration, durationClock)
	}
	if runErr != nil {
		runErr = wrapSessionRuntimeError(plan, wrapSessionPhaseError("run session loop", runErr))
	}
	return runErr
}
