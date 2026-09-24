package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// RunSessionWithInstructions resolves the ask-path system-prompt contract and
// applies the result to the realtime session before the first user turn.
//
// Session instructions intentionally disable dynamic system information. A
// realtime session's instructions are the configured workspace or explicit
// prompt content, while the provider/session runtime continues to own its
// model configuration.
func RunSessionWithInstructions(ctx context.Context, out io.Writer, opts SessionRunOptions, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	// A pure replay has no provider session to configure. Preserve its captured
	// outbound sequence; injected replay sessions remain configurable for tests
	// and caller-owned session seams.
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}

	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

// RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration preserves the
// session command's audio, explicit text-seed, and duration behavior while
// carrying the selected or default workspace instructions into provider
// construction.
func RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if audioPath != "" {
		return ErrLegacyAudioRuntimeRetired
	}
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		if maxDuration == 0 {
			return RunSessionWithTextSeed(ctx, out, opts, seed)
		}
		return RunSessionWithTextSeedAndMaxDuration(ctx, out, opts, maxDuration, seed)
	}
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	instructions, err := resolveSessionInstructions(opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructions(opts, instructions)
	if err != nil {
		return err
	}

	if !seed.Present {
		return runSessionPlanWithDuration(ctx, out, plan, maxDuration, nil, nil)
	}
	wirePrompt := nextSessionTextWirePrompt()
	if maxDuration > 0 {
		return runSessionPlanWithTextSeed(ctx, out, plan, maxDuration, seed.Value, wirePrompt)
	}
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	if plan.inferencer != nil {
		plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed.Value}
	}
	return errors.Join(plan.run(ctx, output), output.errorValue())
}

// RunSessionWithMaxDuration runs a session with an optional graceful duration
// bound. A zero duration preserves the unbounded session; a positive bound is
// owned by the sessionduration service.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, nil)
}

// RunSessionWithMaxDurationClock is the deterministic-clock seam for the
// duration path. Production callers use RunSessionWithMaxDuration.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	plan, err := planBoundedSession(ctx, opts)
	if err != nil {
		return err
	}
	return runSessionPlanWithDuration(ctx, out, plan, maxDuration, clock, nil)
}

// RunSessionWithTextSeedAndMaxDuration preserves the explicit --prompt seed
// behavior while applying the duration admission boundary around the seed
// wrapper. A zero duration delegates to the existing text-seed path.
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
	opts.Prompt, opts.PromptProvided = seed.Value, true
	plan, err := planBoundedSession(ctx, opts)
	if err != nil {
		return err
	}
	return runSessionPlanWithTextSeed(ctx, out, plan, maxDuration, seed.Value, nextSessionTextWirePrompt())
}

// planBoundedSession validates and plans a session for a duration entrypoint.
func planBoundedSession(ctx context.Context, opts SessionRunOptions) (sessionRuntimePlan, error) {
	//nolint:contextcheck // Option validation has no context-aware seam; planning below honors ctx.
	if err := validateSessionRunOptions(opts); err != nil {
		return sessionRuntimePlan{}, err
	}
	return planSessionRuntimeContext(ctx, opts)
}

// runSessionPlanWithTextSeed substitutes the explicit seed inside the duration
// admission boundary: the bounded runner connects through the admitted
// inferencer, so a wrapper composed outside it would leak the wire sentinel.
func runSessionPlanWithTextSeed(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, seed, wirePrompt string) error {
	plan.loop.Prompt = wirePrompt
	output := &sessionTextOutput{writer: out}
	err := runSessionPlanWithDuration(ctx, output, plan, maxDuration, nil, func(inner messages.SessionInferencer) messages.SessionInferencer {
		return &sessionTextSeedInferencer{inner: inner, wirePrompt: wirePrompt, value: seed}
	})
	return errors.Join(err, output.errorValue())
}

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

// resolveSessionInstructions is a compatibility adapter around the reusable
// session instruction service. Filesystem policy normalization remains at the
// CLI host edge; prompt selection, skills ordering, scope formatting and all
// model-facing policy decisions live behind the runtime contract.
func resolveSessionInstructions(opts SessionRunOptions, systemPrompt string) (string, error) {
	return resolveSessionInstructionsContext(context.Background(), opts, systemPrompt)
}

func resolveSessionInstructionsContext(ctx context.Context, opts SessionRunOptions, systemPrompt string) (string, error) {
	request, err := newSessionInstructionRequest(opts, systemPrompt)
	if err != nil {
		return "", err
	}
	result, err := runtimeSessionWire.NewInstructionService().Resolve(ctx, request)
	if err != nil {
		return "", err
	}
	return result.Instructions, nil
}

func newSessionInstructionRequest(opts SessionRunOptions, systemPrompt string) (runtimeSession.InstructionRequest, error) {
	workDir := opts.WorkDir
	if workDir == "" && opts.FilesystemPolicy == nil {
		// Preserve the direct service API's historical workspace behavior. CLI
		// sessions always supply the launch-captured policy explicitly.
		workDir = opts.ConfigDir
	}
	if workDir != "" && opts.FilesystemPolicy == nil {
		// Validate the host-selected workspace before attempting prompt
		// discovery. A missing workspace is a startup/configuration error, not
		// an empty prompt, and must prevent provider/session admission.
		policy, policyErr := cliTools.ResolveFilesystemPolicy(workDir, opts.AllowPaths...)
		if policyErr != nil {
			return runtimeSession.InstructionRequest{}, fmt.Errorf("resolve filesystem scope: %w", policyErr)
		}
		workDir = policy.PrimaryRoot()
	}
	request := runtimeSession.InstructionRequest{
		Prompt:       systemPrompt,
		WorkspaceDir: workDir,
		Loader:       sessionInstructionLoader{workspaceDir: workDir, configDir: opts.ConfigDir},
	}
	if opts.FilesystemPolicy != nil {
		request.FilesystemScopeSet = true
		request.FilesystemScopeDescription = opts.FilesystemPolicy.ScopeDescription()
	}
	return request, nil
}

type sessionInstructionLoader struct {
	workspaceDir string
	configDir    string
}

func (l sessionInstructionLoader) Stat(path string) error { _, err := os.Stat(path); return err }

func (l sessionInstructionLoader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (l sessionInstructionLoader) SkillsSummary() (string, error) {
	return skills.NewLoader(l.workspaceDir, l.configDir).BuildSummary()
}

// composeSessionInstructions is a compatibility adapter around the runtime
// service. Keeping this callable helper preserves the existing planner seams
// without retaining a second policy implementation in the CLI.
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	return runtimeSessionWire.NewInstructionService().Compose(runtimeSession.InstructionComposition{
		Instructions:           instructions,
		ToolDefinitions:        append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
		BrowserCapabilityState: runtimeSession.BrowserCapabilityState(string(opts.BrowserCapabilityState)),
		BrowserToolsEnabled:    opts.BrowserToolsEnabled,
		PageSightToolID:        cliTools.PageSightToolID,
	})
}
