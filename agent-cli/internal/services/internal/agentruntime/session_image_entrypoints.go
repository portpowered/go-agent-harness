package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type SessionImageRunOptions struct {
	SessionRunOptions
	ImagePaths   []string
	AudioOutPath string
	MaxDuration  time.Duration
	TextSeed     sessionturn.Seed
	SystemPrompt string
}

func RunSessionWithImages(ctx context.Context, out io.Writer, opts SessionImageRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()

	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSession(ctx, out, opts.SessionRunOptions)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { releaseSessionClaim(claim, &runErr) }()
	if opts.AudioOutPath != "" {
		opts.SessionRunOptions.AudioOutputRequested = true
	}
	var parts []messages.ImagePart
	var imageCleanup func() error
	opts.SessionRunOptions, parts, imageCleanup, err = prepareSessionImageRun(ctx, opts.SessionRunOptions, paths, opts.TextSeed)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCleanup = imageCleanup
	imageCleanupOwned := true
	defer func() {
		if imageCleanupOwned {
			runErr = errors.Join(runErr, imageCleanup())
		}
	}()
	plan, wirePrompt, err := planSessionImageRuntimeWithContext(ctx, opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, false)
	if err != nil {
		return err
	}
	imageCleanupOwned = false
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}

func RunSessionWithImagesAndAudioInput(ctx context.Context, out io.Writer, opts SessionImageRunOptions, input SessionAudioInput) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()

	if !sessionAudioInputSelected(input) {
		return RunSessionWithImages(ctx, out, opts)
	}
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx, out, opts.SessionRunOptions, opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, input, opts.SystemPrompt)
	}
	return runSessionImagesAudioInput(ctx, out, opts, input, paths)
}

func runSessionImagesAudioInput(ctx context.Context, out io.Writer, opts SessionImageRunOptions, input SessionAudioInput, paths []string) (runErr error) {
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { releaseSessionClaim(claim, &runErr) }()
	var parts []messages.ImagePart
	var imageCleanup func() error
	opts.SessionRunOptions, parts, imageCleanup, err = prepareSessionImageRun(ctx, opts.SessionRunOptions, paths, opts.TextSeed)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCleanup = imageCleanup
	imageCleanupOwned := true
	defer func() {
		if imageCleanupOwned {
			runErr = errors.Join(runErr, imageCleanup())
		}
	}()
	audioSource, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := audioSource.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	opts.SessionRunOptions.ClientOwnsAudioTurnBoundaries = true
	if opts.AudioOutPath != "" {
		opts.SessionRunOptions.AudioOutputRequested = true
	}
	plan, wirePrompt, err := planSessionImageRuntimeWithContext(ctx, opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, true)
	if err != nil {
		return err
	}
	imageCleanupOwned = false
	plan.loop.CloseAfterOpen = false
	plan.loop.AudioIn = audioSource
	plan.loop.MaxDuration = opts.MaxDuration
	plan.loop.RequireAssistantResponse = true
	plan.loop.RequireTerminalAssistantResponse = true
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}

func planSessionImageRuntime(opts SessionRunOptions, parts []messages.ImagePart, seed sessionturn.Seed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, error) {
	return planSessionImageRuntimeWithContext(context.Background(), opts, parts, seed, systemPrompt, deferResponse)
}

func planSessionImageRuntimeWithContext(ctx context.Context, opts SessionRunOptions, parts []messages.ImagePart, seed sessionturn.Seed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, error) {
	var (
		plan         sessionRuntimePlan
		err          error
		instructions string
	)
	if opts.ReplayPath != "" {
		plan, err = planSessionRuntimeWithContext(ctx, opts)
	} else {
		instructions, err = sessionInstructionText(ctx, opts, systemPrompt)
		if err == nil {
			plan, err = planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
		}
	}
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	return attachSessionImageRuntime(ctx, plan, parts, seed, deferResponse, opts.Prompt, opts.sessionImageCleanup)
}

func planSessionImageRuntimeForDirectory(ctx context.Context, opts SessionRunOptions, parts []messages.ImagePart, seed sessionturn.Seed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, func(), error) {
	plan, cleanup, err := planSessionForDirectoryRecordingWithInstructions(ctx, opts, systemPrompt, true)
	if err != nil {
		return sessionRuntimePlan{}, "", func() {}, err
	}
	plan, wirePrompt, err := attachSessionImageRuntime(ctx, plan, parts, seed, deferResponse, opts.Prompt, opts.sessionImageCleanup)
	if err != nil {
		cleanup()
		return sessionRuntimePlan{}, "", func() {}, err
	}
	return plan, wirePrompt, cleanup, nil
}

func attachSessionImageRuntime(ctx context.Context, plan sessionRuntimePlan, parts []messages.ImagePart, seed sessionturn.Seed, deferResponse bool, prompt string, imageCleanup func() error) (sessionRuntimePlan, string, error) {
	if plan.inferencer == nil {
		return sessionRuntimePlan{}, "", errors.New("session image runtime has no session inferencer")
	}
	firstTurn := make(chan error, 1)
	plan.loop.awaitFirstTurn = firstTurn
	turnRuntime, err := newSessionTurnService().Prepare(ctx, sessionturn.Request{
		SessionInferencer: plan.inferencer,
		Seed:              seed,
		Image: &sessionturn.ImageRequest{
			Parts:               parts,
			DeferResponse:       deferResponse,
			FirstTurn:           firstTurn,
			PromptSentinel:      sessionturn.ImageOnlyPrompt,
			DeferredInstruction: sessionturn.DeferredImageInstruction,
		},
		ToolExecutor:    plan.loop.ToolExecutor,
		ToolDefinitions: nil,
		ImageCleanup:    imageCleanup,
	})
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	plan.turnRuntime = turnRuntime
	plan.loop.turnRuntime = turnRuntime
	plan.inferencer = turnRuntime.Inferencer()
	if seed.Present {
		return plan, turnRuntime.WirePrompt(), nil
	}
	if prompt == "" {
		plan.loop.Prompt = sessionturn.ImageOnlyPrompt
	}
	return plan, "", nil
}

func runSessionImagePlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, opts SessionImageRunOptions, wirePrompt string) (runErr error) {
	if opts.AudioOutPath != "" {
		return runSessionImageWithAudioOutput(ctx, out, plan, opts, wirePrompt)
	}
	if opts.TextSeed.Present {
		return runSessionImageWithTextSeed(ctx, out, plan, opts)
	}
	return runSessionImageWithoutSeed(ctx, out, plan, opts.MaxDuration)
}

func runSessionImageWithAudioOutput(ctx context.Context, out io.Writer, plan sessionRuntimePlan, opts SessionImageRunOptions, wirePrompt string) (runErr error) {
	audioOut, err := newSessionAudioOutputForPlan(&plan, opts.AudioOutPath, out, nil)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, err)
	}
	defer func() {
		if closeErr := audioOut.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, closeErr))
		}
	}()
	wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, opts.TextSeed.Value)
	plan.inferencer = wrapped
	if opts.AudioOutPath == "-" {
		out = io.Discard
	}
	if opts.MaxDuration == 0 || plan.loop.AudioIn != nil {
		runErr = plan.run(ctx, out)
	} else {
		runErr = runSessionImageDuration(ctx, out, plan, opts.MaxDuration)
	}
	wrapped.wait()
	if outputErr := wrapped.err(); outputErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", opts.AudioOutPath, outputErr))
	}
	return runErr
}

func runSessionImageWithTextSeed(ctx context.Context, out io.Writer, plan sessionRuntimePlan, opts SessionImageRunOptions) error {
	if plan.turnRuntime == nil {
		return errors.New("session image runtime has no turn service")
	}
	output := plan.turnRuntime.NewOutput(out)
	if opts.MaxDuration == 0 || plan.loop.AudioIn != nil {
		return errors.Join(plan.run(ctx, output), output.Err())
	}
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	admittedInferencer := admitSessionDurationInferencer(&plan)
	err = runSessionDurationPlanWithAdmission(durationCtx, output, plan, opts.MaxDuration, realSessionDurationClock{}, admittedInferencer)
	return errors.Join(err, output.Err())
}

func runSessionImageWithoutSeed(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	if maxDuration == 0 || plan.loop.AudioIn != nil {
		return plan.run(ctx, out)
	}
	return runSessionImageDuration(ctx, out, plan, maxDuration)
}

func runSessionImageDuration(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
}

func prepareSessionImageRun(ctx context.Context, opts SessionRunOptions, sourcePaths []string, seed sessionturn.Seed) (SessionRunOptions, []messages.ImagePart, func() error, error) {
	metadata, err := resolveSessionImageCapabilities(opts)
	if err != nil {
		return opts, nil, noOpSessionImageCleanup, err
	}
	opts.sessionImageCapabilities = cloneSessionImageCapabilities(&metadata)
	stagingRoot := ""
	if sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		stagingRoot, err = sessionImageStagingConfigDir(opts.ConfigDir)
		if err != nil {
			return opts, nil, noOpSessionImageCleanup, err
		}
	}
	prepared, err := newSessionTurnService().PrepareImage(ctx, sessionturn.ImagePreparationRequest{
		SourcePaths:            sourcePaths,
		Capabilities:           metadata,
		StagingRoot:            stagingRoot,
		ToolExecutor:           opts.ToolExecutor,
		ToolDefinitions:        opts.ToolDefinitions,
		RefreshToolDefinitions: opts.RefreshToolDefinitions,
	})
	if err != nil {
		return opts, nil, noOpSessionImageCleanup, err
	}
	opts.ToolExecutor = prepared.ToolExecutor
	opts.ToolDefinitions = prepared.ToolDefinitions
	opts.RefreshToolDefinitions = prepared.RefreshToolDefinitions
	if seed.Present {
		opts.Prompt = seed.Value
		opts.PromptProvided = true
	}
	cleanup := prepared.Cleanup
	if cleanup == nil {
		cleanup = noOpSessionImageCleanup
	}
	return opts, prepared.Parts, cleanup, nil
}

func noOpSessionImageCleanup() error { return nil }
