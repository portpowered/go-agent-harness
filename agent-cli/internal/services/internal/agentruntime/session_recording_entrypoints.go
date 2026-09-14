package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func sessionEvidenceCredentials(opts SessionRunOptions, provider string) []string {
	credentials := make([]string, 0, 2)
	appendCredential := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || containsSessionCredential(credentials, value) {
			return
		}
		credentials = append(credentials, value)
	}
	appendCredential(opts.APIKey)
	if opts.LoadedConfig != nil {
		appendConfiguredSessionCredential(opts.LoadedConfig, provider, appendCredential)
	}
	return credentials
}

func containsSessionCredential(credentials []string, value string) bool {
	for _, existing := range credentials {
		if existing == value {
			return true
		}
	}
	return false
}

func appendConfiguredSessionCredential(loaded *config.Config, provider string, appendCredential func(string)) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case sessionProviderOpenAI:
		if loaded.Model.OpenAI != nil {
			appendCredential(loaded.Model.OpenAI.APIKey)
		}
	case sessionProviderGrok:
		if loaded.Model.Grok != nil {
			appendCredential(loaded.Model.Grok.APIKey)
		}
	}
}

// RunSessionWithRecordingDirectory is the directory-recording entry point for
// callers that do not need optional audio, prompt, or duration seams.
func RunSessionWithRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, directory string) error {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, "", 0, SessionTextSeed{}, "", false, nil)
}

func RunSessionWithRecordingDirectoryAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed) error {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, "", false, nil)
}

func RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) error {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, nil)
}

func RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) error {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt)
	}
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, &input)
}

func RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, audioPaths []string, systemPrompt string) error {
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(audioPaths)); err != nil {
		return err
	}
	if len(audioPaths) == 0 {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt)
	}
	if err := validateSessionRecordingOptions(opts); err != nil {
		return err
	}
	scheduled, err := prepareScheduledAudioInputsContext(ctx, audioPaths)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.Prompt) != "" || seed.Present {
		for index := range scheduled {
			scheduled[index].AfterCompletedTurns++
		}
	}
	opts.AudioInputs, opts.WaitForClose = scheduled, true
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, nil)
}

func RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, audioPaths []string, systemPrompt string) error {
	if len(audioPaths) == 0 {
		opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, opts.SystemPrompt = audioOutPath, maxDuration, seed, systemPrompt
		return RunSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory)
	}
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(audioPaths)); err != nil {
		return err
	}
	if err := validateSessionRecordingOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	scheduled, err := prepareScheduledAudioInputsContext(ctx, audioPaths)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.AudioInputs, opts.SessionRunOptions.WaitForClose = scheduled, true
	opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, opts.SystemPrompt = audioOutPath, maxDuration, seed, systemPrompt
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, nil)
}

func RunSessionWithImagesAndRecordingDirectory(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory string) error {
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, nil)
}

func RunSessionWithImagesAndRecordingDirectoryAndAudioInput(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory string, input SessionAudioInput) error {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory)
	}
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, &input)
}

func runSessionWithImagesAndRecordingDirectory(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory string, audioInput *SessionAudioInput) (runErr error) {
	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return runSessionWithRecordingDirectory(ctx, out, opts.SessionRunOptions, directory, opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, opts.SystemPrompt, true, audioInput)
	}
	if err := validateImageRecordingRun(opts); err != nil {
		return err
	}
	metadata, err := resolveSessionImageCapabilities(opts.SessionRunOptions)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.sessionImageCapabilities = cloneSessionImageCapabilities(&metadata)
	parts, err := PrepareSessionImageParts(paths, metadata)
	if err != nil {
		return err
	}
	if opts.TextSeed.Present {
		opts.SessionRunOptions.Prompt, opts.SessionRunOptions.PromptProvided = opts.TextSeed.Value, true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccessContext(ctx, opts.SessionRunOptions, paths, parts)
	if err != nil {
		return err
	}
	defer imageCleanup()
	audioSource, err := openOptionalSessionAudioInput(audioInput)
	if err != nil {
		return err
	}
	if audioSource != nil {
		defer func() { runErr = errors.Join(runErr, audioSource.Close()) }()
		opts.SessionRunOptions.ClientOwnsAudioTurnBoundaries = true
	}
	plan, wirePrompt, cleanup, err := planSessionImageRuntimeForDirectoryContext(ctx, opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, audioSource != nil || len(opts.SessionRunOptions.AudioInputs) > 0)
	if err != nil {
		return err
	}
	defer cleanup()
	configureImageAudioPlan(&plan, audioSource, opts.MaxDuration)
	recorder, err := openSessionLiveRecorder(opts.SessionRunOptions, plan, directory, opts.MaxDuration)
	if err != nil {
		return err
	}
	plan.liveRecorder = recorder
	stopBrowserRecording := startBrowserRecording(ctx, opts.SessionRunOptions, recorder)
	recorderFinalized := false
	defer finalizeSessionDirectoryRecorder(ctx, &runErr, recorder, stopBrowserRecording, &recorderFinalized)
	runErr = runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
	recorderFinalized = true
	return runErr
}

func validateImageRecordingRun(opts SessionImageRunOptions) error {
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	return validateSessionRecordingOptions(opts.SessionRunOptions)
}

func openOptionalSessionAudioInput(input *SessionAudioInput) (*sessionAudioSource, error) {
	if input == nil {
		return nil, nil
	}
	if err := validateSessionAudioInput(*input); err != nil {
		return nil, err
	}
	return openSessionAudioInput(*input)
}

func configureImageAudioPlan(plan *sessionRuntimePlan, source *sessionAudioSource, maxDuration time.Duration) {
	if source == nil {
		return
	}
	source.bindRuntime(plan.runtime, plan.clockSource)
	plan.loop.CloseAfterOpen, plan.loop.AudioIn, plan.loop.MaxDuration = false, source, maxDuration
	plan.loop.RequireAssistantResponse, plan.loop.RequireTerminalAssistantResponse = true, true
}

func runSessionWithRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string, withInstructions bool, audioInput *SessionAudioInput) (runErr error) {
	if strings.TrimSpace(directory) == "" {
		return runSessionWithoutRecordingDirectory(ctx, out, opts, audioOutPath, maxDuration, seed, systemPrompt, withInstructions)
	}
	if err := validateDirectoryRecordingRun(opts, maxDuration); err != nil {
		return err
	}
	if audioInput != nil {
		opts.ClientOwnsAudioTurnBoundaries = true
	}
	if audioOutPath != "" {
		opts.AudioOutputRequested = true
	}
	plan, cleanup, err := planSessionForDirectoryRecordingWithInstructionsContext(ctx, opts, systemPrompt, withInstructions)
	if err != nil {
		return err
	}
	defer cleanup()
	audioSource, err := openOptionalSessionAudioInput(audioInput)
	if err != nil {
		return err
	}
	if audioSource != nil {
		defer func() { runErr = errors.Join(runErr, audioSource.Close()) }()
		configureDirectoryAudioPlan(&plan, audioSource, maxDuration)
	}
	recorder, err := openSessionLiveRecorder(opts, plan, directory, maxDuration)
	if err != nil {
		return err
	}
	plan.liveRecorder = recorder
	stopBrowserRecording := startBrowserRecording(ctx, opts, recorder)
	recorderFinalized := false
	defer finalizeSessionDirectoryRecorder(ctx, &runErr, recorder, stopBrowserRecording, &recorderFinalized)
	audioOutput, audioWrapper, textOutput, err := configureSessionOutputs(&plan, out, audioOutPath, seed)
	if err != nil {
		return err
	}
	sessionOut := sessionOutputWriter(out, audioOutPath, textOutput)
	if audioSource != nil || maxDuration == 0 {
		runErr = plan.run(ctx, sessionOut)
	} else {
		runErr = runSessionDuration(ctx, sessionOut, plan, maxDuration)
	}
	runErr = finishSessionOutputs(runErr, audioOutput, audioWrapper, audioOutPath)
	recorderFinalized = true
	return runErr
}

func runSessionWithoutRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string, withInstructions bool) error {
	if withInstructions {
		return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed, systemPrompt)
	}
	return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed)
}

func validateDirectoryRecordingRun(opts SessionRunOptions, maxDuration time.Duration) error {
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	return validateSessionRecordingOptions(opts)
}

func configureDirectoryAudioPlan(plan *sessionRuntimePlan, source *sessionAudioSource, maxDuration time.Duration) {
	source.bindRuntime(plan.runtime, plan.clockSource)
	plan.loop.CloseAfterOpen, plan.loop.AudioIn, plan.loop.MaxDuration = false, source, maxDuration
	plan.loop.RequireAssistantResponse = true
}

func finalizeSessionDirectoryRecorder(ctx context.Context, runErr *error, recorder runtimesession.LiveRecorder, stop func(), finalized *bool) {
	stop()
	if recorder != nil && !*finalized {
		*runErr = errors.Join(*runErr, recorder.Finalize(context.WithoutCancel(ctx), *runErr))
	}
}

func configureSessionOutputs(plan *sessionRuntimePlan, out io.Writer, audioOutPath string, seed SessionTextSeed) (*sessionAudioOutput, *sessionAudioOutputInferencer, *sessionTextOutput, error) {
	if audioOutPath != "" {
		audioOutput, err := newSessionAudioOutputForPlan(plan, audioOutPath, out, nil)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("--audio-out %q: %w", audioOutPath, err)
		}
		if plan.inferencer == nil {
			return audioOutput, nil, nil, nil
		}
		wirePrompt := ""
		if seed.Present {
			wirePrompt = nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
		}
		wrapper := newSessionAudioOutputInferencer(plan.inferencer, audioOutput, wirePrompt, seed.Value)
		plan.inferencer = wrapper
		return audioOutput, wrapper, nil, nil
	}
	if !seed.Present {
		return nil, nil, nil, nil
	}
	wirePrompt := nextSessionTextWirePrompt()
	plan.loop.Prompt = wirePrompt
	if plan.inferencer == nil {
		return nil, nil, nil, nil
	}
	textOutput := &sessionTextOutput{writer: out}
	plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed.Value}
	return nil, nil, textOutput, nil
}

func sessionOutputWriter(out io.Writer, audioOutPath string, textOutput *sessionTextOutput) io.Writer {
	if audioOutPath == "-" {
		return io.Discard
	}
	if textOutput != nil {
		return textOutput
	}
	return out
}

func runSessionDuration(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration) error {
	durationCtx, err := prepareSessionDurationArtifacts(ctx)
	if err != nil {
		return err
	}
	return runSessionDurationPlan(durationCtx, out, plan, maxDuration, realSessionDurationClock{})
}

func finishSessionOutputs(runErr error, audioOutput *sessionAudioOutput, audioWrapper *sessionAudioOutputInferencer, audioOutPath string) error {
	if audioWrapper != nil {
		audioWrapper.wait()
		runErr = errors.Join(runErr, audioWrapper.err())
	}
	if audioOutput != nil {
		if err := audioOutput.close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, err))
		}
	}
	return runErr
}

func validateSessionRecordingOptions(opts SessionRunOptions) error {
	if opts.RecordPath == "" && opts.ReplayPath == "" {
		return nil
	}
	return validateSessionRunOptions(opts)
}

func planSessionForDirectoryRecordingWithInstructions(opts SessionRunOptions, systemPrompt string, withInstructions bool) (sessionRuntimePlan, func(), error) {
	return planSessionForDirectoryRecordingWithInstructionsContext(context.Background(), opts, systemPrompt, withInstructions)
}

func planSessionForDirectoryRecordingWithInstructionsContext(ctx context.Context, opts SessionRunOptions, systemPrompt string, withInstructions bool) (sessionRuntimePlan, func(), error) {
	plan, err := planDirectoryRuntime(ctx, opts, systemPrompt, withInstructions)
	if err != nil {
		return sessionRuntimePlan{}, func() {}, err
	}
	if opts.RecordPath != "" && opts.SessionInferencer != nil {
		plan = wrapDirectoryFixtureRecorder(plan, opts.RecordPath)
	}
	return plan, func() {}, nil
}

func planDirectoryRuntime(ctx context.Context, opts SessionRunOptions, systemPrompt string, withInstructions bool) (sessionRuntimePlan, error) {
	if !withInstructions || (opts.ReplayPath != "" && opts.SessionInferencer == nil) {
		return planSessionRuntimeContext(ctx, opts)
	}
	instructions, err := resolveSessionInstructionsContext(ctx, opts, systemPrompt)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	return planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
}

func wrapDirectoryFixtureRecorder(plan sessionRuntimePlan, path string) sessionRuntimePlan {
	fixture := gwtesting.NewRecordingSessionInferencer(plan.inferencer)
	plan.mode = sessionRuntimeModeRecordGrok
	if strings.EqualFold(plan.provider, sessionProviderOpenAI) {
		plan.mode = sessionRuntimeModeRecordOpenAI
	}
	plan.capturePath, plan.inferencer = path, fixture
	plan.flushCapture = func() error {
		if recorder := fixture.Recorder(); recorder != nil {
			return recorder.FlushToFile(path)
		}
		return errors.New("session fixture recorder did not connect")
	}
	plan.flushCaptureTo = func(destination string) error {
		if recorder := fixture.Recorder(); recorder != nil {
			return recorder.FlushToFile(destination)
		}
		return errors.New("session fixture recorder did not connect")
	}
	return plan
}
