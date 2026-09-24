package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
)

// RunSessionWithMaxDuration runs a session with a service-owned optional
// duration bound. A zero duration keeps the existing unbounded plan path.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, nil)
}

// RunSessionWithMaxDurationClock exposes the deterministic scheduler seam used
// by public duration behavior tests and injected runtime callers.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, clock duration.TimerScheduler) (runErr error) {
	service := durationwire.NewService()
	if err := service.ValidateDuration(maxDuration); err != nil {
		return err
	}
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { releaseSessionClaim(claim, &runErr) }()
	plan, err := planSessionRuntimeWithContext(ctx, opts)
	if err != nil {
		return err
	}
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := service.PrepareArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, clock)
}

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
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()

	instructions, err := sessionInstructionText(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}

	plan, err := planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
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
	if opts.ReplayPath != "" && opts.SessionInferencer == nil {
		return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioPath, maxDuration, seed)
	}
	applyInstructionAudioOptions(&opts, audioPath, seed)
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	instructions, err := sessionInstructionText(ctx, opts, systemPrompt)
	if err != nil {
		return err
	}
	plan, err := planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
	if err != nil {
		return err
	}
	if audioPath == "" {
		return runSessionInstructionsWithoutAudio(ctx, out, plan, maxDuration, seed)
	}
	return runSessionInstructionsWithAudio(ctx, out, plan, audioPath, maxDuration, seed)
}

func applyInstructionAudioOptions(opts *SessionRunOptions, audioPath string, seed SessionTextSeed) {
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	if audioPath != "" {
		opts.AudioOutputRequested = true
	}
}

func runSessionInstructionsWithoutAudio(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, seed SessionTextSeed) error {
	if seed.Present {
		return runSessionInstructionsWithSeed(ctx, out, plan, maxDuration, seed)
	}
	return runSessionInstructionsDurationOrPlan(ctx, out, plan, maxDuration)
}

func runSessionInstructionsWithSeed(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, seed SessionTextSeed) error {
	turnRuntime, err := prepareSessionTurnSeed(ctx, &plan, seed)
	if err != nil {
		return err
	}
	if turnRuntime == nil {
		return plan.run(ctx, out)
	}
	output := turnRuntime.NewOutput(out)
	if maxDuration == 0 {
		return errors.Join(plan.run(ctx, output), output.Err())
	}
	durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
	if err != nil {
		return err
	}
	// The seed substitution wrapper must sit inside the admission boundary so
	// the duration runner never puts its wire sentinel on the live provider.
	admittedInferencer := admitSessionDurationInferencer(&plan)
	runErr := runSessionDurationPlan(durationCtx, output, plan, maxDuration, nil, admittedInferencer)
	return errors.Join(runErr, output.Err())
}

func admitSessionDurationInferencer(plan *sessionRuntimePlan) duration.AdmissionInferencer {
	if plan.inferencer == nil {
		return nil
	}
	service := durationwire.NewService()
	admitted := service.NewAdmissionInferencer(plan.inferencer, service.NewEventAdmission(), make(chan struct{}))
	plan.inferencer = admitted
	return admitted
}

func runSessionInstructionsWithAudio(ctx context.Context, out io.Writer, plan sessionRuntimePlan, audioPath string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	turnRuntime, err := prepareSessionTurnSeed(ctx, &plan, seed)
	if err != nil {
		return err
	}
	audioOut, err := newSessionAudioOutputForPlan(ctx, &plan, audioPath, out, nil)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", audioPath, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, closeErr))
		}
	}()
	sessionOut := out
	if audioPath == "-" {
		sessionOut = io.Discard
	}
	if plan.inferencer == nil {
		return runSessionInstructionsDurationOrPlan(ctx, sessionOut, plan, maxDuration)
	}
	return runSessionInstructionsWithAudioInferencer(ctx, sessionOut, plan, audioOut, audioPath, maxDuration, turnRuntime)
}

func runSessionInstructionsWithAudioInferencer(ctx context.Context, out io.Writer, plan sessionRuntimePlan, audioOut *sessionAudioOutput, audioPath string, maxDuration time.Duration, turnRuntime sessionturn.Runtime) (runErr error) {
	wrapped, err := attachSessionAudioOutput(turnRuntime, &plan, audioOut)
	if err != nil {
		return err
	}
	runErr = runSessionInstructionsDurationOrPlan(ctx, out, plan, maxDuration)
	if outputErr := wrapped.Wait(); outputErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioPath, outputErr))
	}
	return runErr
}

func runSessionInstructionsDurationOrPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	durationCtx, err := durationwire.NewService().PrepareArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, nil)
}

// sessionInstructionText requests the reusable
// session instruction service. Filesystem policy normalization remains at the
// CLI host edge; prompt selection, skills ordering, scope formatting and all
// model-facing policy decisions live behind the runtime contract.
func sessionInstructionText(ctx context.Context, opts SessionRunOptions, systemPrompt string) (string, error) {
	request, err := newSessionInstructionRequest(opts, systemPrompt)
	if err != nil {
		return "", err
	}
	return sessionturnwire.NewDefaultService().ResolveInstructions(ctx, sessionturn.InstructionRequest{Request: request})
}

func newSessionInstructionRequest(opts SessionRunOptions, systemPrompt string) (runtimeSession.InstructionRequest, error) {
	workDir := opts.WorkDir
	if workDir == "" && opts.FilesystemPolicy == nil {
		workDir = opts.ConfigDir
	}
	request := runtimeSession.InstructionRequest{
		Prompt:       systemPrompt,
		WorkspaceDir: workDir,
		ConfigDir:    opts.ConfigDir,
	}
	if opts.FilesystemPolicy != nil {
		request.WorkspaceDir = opts.FilesystemPolicy.PrimaryRoot()
		request.FilesystemScopeSet = true
		request.FilesystemScopeDescription = opts.FilesystemPolicy.ScopeDescription()
	}
	return request, nil
}

// composeSessionInstructions is a compatibility adapter around the runtime
// service. Keeping this callable helper preserves the existing planner seams
// without retaining a second policy implementation in the CLI.
func composeSessionInstructions(opts SessionRunOptions, instructions string) string {
	resolved, err := sessionturnwire.NewDefaultService().ResolveInstructions(context.Background(), sessionturn.InstructionRequest{
		Text: instructions,
		Composition: &runtimeSession.InstructionComposition{
			ToolDefinitions:        append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
			BrowserCapabilityState: runtimeSession.BrowserCapabilityState(string(opts.BrowserCapabilityState)),
			BrowserToolsEnabled:    opts.BrowserToolsEnabled,
			PageSightToolID:        cliTools.PageSightToolID,
		},
	})
	if err != nil {
		return instructions
	}
	return resolved
}
