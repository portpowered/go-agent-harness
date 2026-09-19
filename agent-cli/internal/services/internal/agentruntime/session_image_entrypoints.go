package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
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
	firstTurn := make(chan error, 1)
	opts.sessionImageRequest = &sessionturn.Request{Seed: seed, Image: &sessionturn.ImageRequest{Parts: append([]messages.ImagePart(nil), parts...), DeferResponse: deferResponse, FirstTurn: firstTurn, PromptSentinel: sessionturn.ImageOnlyPrompt, DeferredInstruction: sessionturn.DeferredImageInstruction}}
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
	opts.sessionImageRequest = &sessionturn.Request{Seed: seed, Image: &sessionturn.ImageRequest{Parts: append([]messages.ImagePart(nil), parts...), DeferResponse: deferResponse, FirstTurn: make(chan error, 1), PromptSentinel: sessionturn.ImageOnlyPrompt, DeferredInstruction: sessionturn.DeferredImageInstruction}}
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
	if plan.turnRuntime == nil {
		return sessionRuntimePlan{}, "", errors.New("session image runtime has no turn service")
	}
	plan.loop.turnRuntime = plan.turnRuntime
	if seed.Present {
		return plan, plan.turnRuntime.WirePrompt(), nil
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
	provider := effectiveSessionProvider(opts)
	model, err := resolveSessionImageModel(opts)
	if err != nil {
		return opts, nil, noOpSessionImageCleanup, err
	}
	configuredModel, err := loadSessionImageModelMetadata(opts.ConfigDir, model)
	if err != nil {
		return opts, nil, noOpSessionImageCleanup, err
	}
	turnService := newSessionTurnService()
	capabilityRequest := sessionturn.ImageCapabilityRequest{
		Provider:        provider,
		Model:           model,
		ModelProvided:   opts.ModelProvided,
		ModelCatalog:    opts.ModelCatalog,
		ConfiguredModel: configuredModel,
	}
	stagingRoot, err := sessionImageStagingConfigDir(opts.ConfigDir)
	if err != nil {
		return opts, nil, noOpSessionImageCleanup, err
	}
	prepared, err := turnService.PrepareImage(ctx, sessionturn.ImagePreparationRequest{
		SourcePaths:            sourcePaths,
		CapabilityRequest:      &capabilityRequest,
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
	capabilities := prepared.Capabilities
	opts.sessionImageCapabilities = &capabilities
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

func resolveSessionImageModel(opts SessionRunOptions) (string, error) {
	model := strings.TrimSpace(opts.Model)
	if model == "" && opts.ReplayPath != "" {
		return openAIRealtimeModel, nil
	}
	if model != "" {
		return model, nil
	}
	resolved, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resolved.Model), nil
}

func loadSessionImageModelMetadata(configDir, model string) (*sessionturn.ImageModelMetadata, error) {
	storage, err := config.NewModelsConfigStorage(configDir)
	if err != nil {
		return nil, fmt.Errorf("initialize model capability metadata: %w", err)
	}
	models, err := storage.Load()
	if err != nil {
		return nil, fmt.Errorf("load model capability metadata: %w", err)
	}
	info := models.Lookup(model)
	if info == nil {
		return nil, nil
	}
	return &sessionturn.ImageModelMetadata{
		InputModalities:         append([]string(nil), info.InputModalities...),
		SupportedInputMIMETypes: append([]string(nil), info.SupportedInputMimeTypes...),
	}, nil
}

func sessionImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(filepath.Dir(storage.Path())), nil
}
