package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
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
	defer func() { _ = claim.release() }()
	metadata, err := resolveSessionImageCapabilities(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCapabilities = cloneSessionImageCapabilities(&metadata)
	parts, err := sessionturnwire.NewDefaultService().PrepareImageParts(paths, metadata)
	if err != nil {
		return err
	}
	if opts.TextSeed.Present {
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
		opts.SessionRunOptions.PromptProvided = true
	}
	if opts.AudioOutPath != "" {
		opts.SessionRunOptions.AudioOutputRequested = true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccess(opts.SessionRunOptions, paths, parts)
	if err != nil {
		return err
	}
	defer imageCleanup()
	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, false)
	if err != nil {
		return err
	}
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
	defer func() { _ = claim.release() }()
	metadata, err := resolveSessionImageCapabilities(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCapabilities = cloneSessionImageCapabilities(&metadata)
	parts, err := sessionturnwire.NewDefaultService().PrepareImageParts(paths, metadata)
	if err != nil {
		return err
	}
	if opts.TextSeed.Present {
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
		opts.SessionRunOptions.PromptProvided = true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccess(opts.SessionRunOptions, paths, parts)
	if err != nil {
		return err
	}
	defer imageCleanup()
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
	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, true)
	if err != nil {
		return err
	}
	plan.loop.CloseAfterOpen = false
	plan.loop.AudioIn = audioSource
	plan.loop.MaxDuration = opts.MaxDuration
	plan.loop.RequireAssistantResponse = true
	plan.loop.RequireTerminalAssistantResponse = true
	return runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
}

func planSessionImageRuntime(opts SessionRunOptions, parts []messages.ImagePart, seed sessionturn.Seed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, error) {
	var (
		plan         sessionRuntimePlan
		err          error
		instructions string
	)
	if opts.ReplayPath != "" {
		plan, err = planSessionRuntime(opts)
	} else {
		instructions, err = sessionInstructionText(opts, systemPrompt)
		if err == nil {
			plan, err = planSessionWithResolvedInstructions(opts, instructions)
		}
	}
	if err != nil {
		return sessionRuntimePlan{}, "", err
	}
	return attachSessionImageRuntime(plan, parts, seed, deferResponse, opts.Prompt)
}

func planSessionImageRuntimeForDirectory(opts SessionRunOptions, parts []messages.ImagePart, seed sessionturn.Seed, systemPrompt string, deferResponse bool) (sessionRuntimePlan, string, func(), error) {
	plan, cleanup, err := planSessionForDirectoryRecordingWithInstructions(opts, systemPrompt, true)
	if err != nil {
		return sessionRuntimePlan{}, "", func() {}, err
	}
	plan, wirePrompt, err := attachSessionImageRuntime(plan, parts, seed, deferResponse, opts.Prompt)
	if err != nil {
		cleanup()
		return sessionRuntimePlan{}, "", func() {}, err
	}
	return plan, wirePrompt, cleanup, nil
}

func attachSessionImageRuntime(plan sessionRuntimePlan, parts []messages.ImagePart, seed sessionturn.Seed, deferResponse bool, prompt string) (sessionRuntimePlan, string, error) {
	if plan.inferencer == nil {
		return sessionRuntimePlan{}, "", errors.New("session image runtime has no session inferencer")
	}
	firstTurn := make(chan error, 1)
	plan.loop.awaitFirstTurn = firstTurn
	turnRuntime, err := sessionturnwire.NewDefaultService().Prepare(context.Background(), sessionturn.Request{
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

func sessionHasTool(definitions []messages.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func bindSessionImageToolExecutor(opts SessionRunOptions, plan sessionRuntimePlan) messages.ToolExecutor {
	if opts.ToolExecutor == nil || !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return opts.ToolExecutor
	}
	binder, ok := opts.ToolExecutor.(runtimeTools.SessionImagePreparerBinder)
	if !ok {
		return opts.ToolExecutor
	}

	capabilities := cloneSessionImageCapabilities(opts.sessionImageCapabilities)
	var resolveErr error
	if capabilities == nil {
		capabilityOpts := opts
		if plan.provider != "" {
			capabilityOpts.Provider = plan.provider
		}
		if plan.model != "" {
			capabilityOpts.Model = plan.model
			capabilityOpts.ModelProvided = true
		}
		var resolved sessionturn.ImageCapabilities
		resolved, resolveErr = resolveSessionImageCapabilities(capabilityOpts)
		capabilities = cloneSessionImageCapabilities(&resolved)
	}
	metadata := sessionturn.ImageCapabilities{}
	if capabilities != nil {
		metadata = *capabilities
		metadata.SupportedInputMIMETypes = append([]string(nil), capabilities.SupportedInputMIMETypes...)
	}
	preparer := runtimeTools.ImagePartPreparer(func(paths []string) ([]messages.ImagePart, error) {
		if resolveErr != nil {
			return nil, resolveErr
		}
		return sessionturnwire.NewDefaultService().PrepareImageParts(paths, metadata)
	})
	return binder.WithSessionImagePreparer(preparer)
}

// prepareSessionImageToolAccess gives read_image a stable, session-owned copy
// of each initial image and advertises those exact paths to the provider. The
// inline image turn still uses the validated parts supplied by the caller;
// staging is only needed for a later model-issued read_image call.
func prepareSessionImageToolAccess(opts SessionRunOptions, sourcePaths []string, parts []messages.ImagePart) (SessionRunOptions, func(), error) {
	if !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return opts, noOpSessionImageCleanup, nil
	}
	if len(sourcePaths) != len(parts) {
		return opts, noOpSessionImageCleanup, fmt.Errorf("stage session images: source path count %d does not match image part count %d", len(sourcePaths), len(parts))
	}

	configDir, err := sessionImageStagingConfigDir(opts.ConfigDir)
	if err != nil {
		return opts, noOpSessionImageCleanup, fmt.Errorf("stage session images: %w", err)
	}
	staged, err := runtimeToolsWire.NewImageStaging().Stage(context.Background(), runtimeTools.ImageStagingRequest{
		StagingRoot:            configDir,
		SourcePaths:            sourcePaths,
		ImageParts:             parts,
		ToolDefinitions:        opts.ToolDefinitions,
		RefreshToolDefinitions: opts.RefreshToolDefinitions,
	})
	if err != nil {
		return opts, noOpSessionImageCleanup, err
	}
	opts.ToolDefinitions = staged.ToolDefinitions
	opts.RefreshToolDefinitions = staged.RefreshToolDefinitions
	cleanup := func() {
		if staged.Cleanup != nil {
			if err := staged.Cleanup(); err != nil {
				log.Printf("stage session images: cleanup: %v", err)
			}
		}
	}
	return opts, cleanup, nil
}

func noOpSessionImageCleanup() {}

func sessionImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		configDir = filepath.Join(homeDir, config.ConfigDirName)
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(abs), nil
}
