package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// RunSessionWithRecordingDirectory is the directory-recording entry point for
// callers that do not need the optional audio-out, prompt, or duration seams.
func RunSessionWithRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, directory string) error {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, "", 0, SessionTextSeed{}, "", false, nil)
}

// RunSessionWithRecordingDirectoryAndAudioOutAndTextSeedAndMaxDuration runs the
// existing session command path with a transparent both-side observer. The
// directory is finalized only after the session and any existing output sinks
// have stopped, so manifest hashes describe the bytes customers receive.
func RunSessionWithRecordingDirectoryAndAudioOutAndTextSeedAndMaxDuration(
	ctx context.Context,
	out io.Writer,
	opts SessionRunOptions,
	directory string,
	audioOutPath string,
	maxDuration time.Duration,
	seed SessionTextSeed,
) (runErr error) {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, "", false, nil)
}

// RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration
// preserves the session instruction path while adding the complete directory
// observer. The older entry point above remains available for callers that do
// not provide an explicit system prompt.
func RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(
	ctx context.Context,
	out io.Writer,
	opts SessionRunOptions,
	directory string,
	audioOutPath string,
	maxDuration time.Duration,
	seed SessionTextSeed,
	systemPrompt string,
) (runErr error) {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, nil)
}

// RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration
// composes the directory observer with the production file/stdin audio-input
// source. Audio frames travel through sessionLoopOptions.AudioIn, so the
// recorder observes the same outbound stream that the provider receives.
func RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(
	ctx context.Context,
	out io.Writer,
	opts SessionRunOptions,
	directory string,
	audioOutPath string,
	maxDuration time.Duration,
	seed SessionTextSeed,
	input SessionAudioInput,
	systemPrompt string,
) (runErr error) {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt)
	}
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, &input)
}

// RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration
// drives multiple finite audio files through one persistent recorded session.
// The existing singular audio-input entry point remains unchanged for callers
// that want one paced source and one response.
func RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
	ctx context.Context,
	out io.Writer,
	opts SessionRunOptions,
	directory string,
	audioOutPath string,
	maxDuration time.Duration,
	seed SessionTextSeed,
	audioPaths []string,
	systemPrompt string,
) (runErr error) {
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(audioPaths)); err != nil {
		return err
	}
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if len(audioPaths) == 0 {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt)
	}
	if err := validateSessionRecordingOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	scheduled, err := prepareScheduledAudioInputs(audioPaths)
	if err != nil {
		return err
	}
	// The positional message (or explicit --prompt seed) is the first user
	// turn. Delay scheduled audio until that response completes so the two
	// caller-provided input surfaces cannot race each other on the provider
	// session's outbound queue.
	if strings.TrimSpace(opts.Prompt) != "" || seed.Present {
		for index := range scheduled {
			scheduled[index].AfterCompletedTurns++
		}
	}
	opts.AudioInputs = scheduled
	opts.WaitForClose = true
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, nil)
}

// RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration
// composes one ordered image turn with the repeatable finite spoken-turn path.
// The image wrapper queues its complete user item without requesting a response;
// the first scheduled audio input then supplies the response boundary, so the
// image is consumed exactly once as part of scheduled turn one.
func RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration(
	ctx context.Context,
	out io.Writer,
	opts SessionImageRunOptions,
	directory string,
	audioOutPath string,
	maxDuration time.Duration,
	seed SessionTextSeed,
	audioPaths []string,
	systemPrompt string,
) (runErr error) {
	if len(audioPaths) == 0 {
		opts.AudioOutPath = audioOutPath
		opts.MaxDuration = maxDuration
		opts.TextSeed = seed
		opts.SystemPrompt = systemPrompt
		return RunSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory)
	}
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(audioPaths)); err != nil {
		return err
	}
	if err := validateSessionRecordingOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	directoryClaim, _, err := ensureSessionRecordingDirectoryClaim(&opts.SessionRunOptions, directory)
	if err != nil {
		return err
	}
	defer func() { _ = directoryClaim.release() }()
	scheduled, err := prepareScheduledAudioInputs(audioPaths)
	if err != nil {
		return err
	}
	// Unlike the ordinary text-plus-scheduled-audio composition, the image
	// wrapper deliberately defers its response. Keep scheduled turn one at
	// AfterCompletedTurns=0 so its audio completes the image turn rather than
	// waiting for a response that has not been requested.
	opts.SessionRunOptions.AudioInputs = scheduled
	opts.SessionRunOptions.WaitForClose = true
	opts.AudioOutPath = audioOutPath
	opts.MaxDuration = maxDuration
	opts.TextSeed = seed
	opts.SystemPrompt = systemPrompt
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, nil)
}

// RunSessionWithImagesAndRecordingDirectory composes the image-turn wrapper
// with the directory observer. The observer stays outside the image wrapper so
// the provider still receives its optional SendMessage image turn while the
// ordinary stream events remain available to the recording session.
func RunSessionWithImagesAndRecordingDirectory(
	ctx context.Context,
	out io.Writer,
	opts SessionImageRunOptions,
	directory string,
) (runErr error) {
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, nil)
}

// RunSessionWithImagesAndRecordingDirectoryAndAudioInput adds the production
// file/stdin audio source to the composed image and directory-recording path.
func RunSessionWithImagesAndRecordingDirectoryAndAudioInput(
	ctx context.Context,
	out io.Writer,
	opts SessionImageRunOptions,
	directory string,
	input SessionAudioInput,
) (runErr error) {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory)
	}
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, &input)
}

func runSessionWithImagesAndRecordingDirectory(
	ctx context.Context,
	out io.Writer,
	opts SessionImageRunOptions,
	directory string,
	audioInput *SessionAudioInput,
) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return runSessionWithRecordingDirectory(ctx, out, opts.SessionRunOptions, directory, opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, opts.SystemPrompt, true, audioInput)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRecordingOptions(opts.SessionRunOptions); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	directoryClaim, destination, err := ensureSessionRecordingDirectoryClaim(&opts.SessionRunOptions, directory)
	if err != nil {
		return err
	}
	defer func() { _ = directoryClaim.release() }()
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
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
		opts.SessionRunOptions.PromptProvided = true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccess(opts.SessionRunOptions, paths, parts)
	if err != nil {
		return err
	}
	defer imageCleanup()
	var audioSource *sessionAudioSource
	if audioInput != nil {
		if err := validateSessionAudioInput(*audioInput); err != nil {
			return err
		}
		audioSource, err = openSessionAudioInput(*audioInput)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := audioSource.Close(); closeErr != nil {
				runErr = errors.Join(runErr, closeErr)
			}
		}()
		opts.SessionRunOptions.ClientOwnsAudioTurnBoundaries = true
	}
	plan, wirePrompt, cleanup, err := planSessionImageRuntimeForDirectory(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, audioSource != nil || len(opts.SessionRunOptions.AudioInputs) > 0)
	if err != nil {
		return err
	}
	defer cleanup()
	if audioSource != nil {
		audioSource.bindRuntime(plan.runtime, plan.clockSource)
		plan.loop.CloseAfterOpen = false
		plan.loop.AudioIn = audioSource
		plan.loop.MaxDuration = opts.MaxDuration
		// A finite image-plus-audio source can produce an intermediate provider
		// response containing a tool call. Keep the session open through the
		// tool result and the follow-up assistant response.
		plan.loop.RequireAssistantResponse = true
		plan.loop.RequireTerminalAssistantResponse = true
	}
	recording := newSessionDirectoryRecording(destination, plan, opts.SessionRunOptions)
	recording.browser.start(ctx)
	plan.loop.toolLifecycleObserver = recording
	plan.loop.terminalSummaryRecorder = recording
	if plan.inferencer != nil {
		plan.inferencer = &sessionDirectoryRecordingInferencer{
			inner:     plan.inferencer,
			recording: recording,
		}
	}
	runErr = runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
	return finalizeSessionDirectoryRecording(runErr, recording)
}
func runSessionWithRecordingDirectory(
	ctx context.Context,
	out io.Writer,
	opts SessionRunOptions,
	directory string,
	audioOutPath string,
	maxDuration time.Duration,
	seed SessionTextSeed,
	systemPrompt string,
	withInstructions bool,
	audioInput *SessionAudioInput,
) (runErr error) {
	opts, coordinator := prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if strings.TrimSpace(directory) == "" {
		if withInstructions {
			return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed, systemPrompt)
		}
		return RunSessionWithAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed)
	}
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if err := validateSessionRecordingOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	directoryClaim, destination, err := ensureSessionRecordingDirectoryClaim(&opts, directory)
	if err != nil {
		return err
	}
	defer func() { _ = directoryClaim.release() }()

	if audioInput != nil {
		// The finite source sends MESSAGE.END after its final frame. Keep the
		// provider from auto-committing the same buffer through server VAD.
		opts.ClientOwnsAudioTurnBoundaries = true
	}
	if audioOutPath != "" {
		opts.AudioOutputRequested = true
	}
	plan, cleanup, err := planSessionForDirectoryRecordingWithInstructions(opts, systemPrompt, withInstructions)
	if err != nil {
		return err
	}
	defer cleanup()

	var audioSource *sessionAudioSource
	if audioInput != nil {
		if err := validateSessionAudioInput(*audioInput); err != nil {
			return err
		}
		audioSource, err = openSessionAudioInput(*audioInput)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := audioSource.Close(); closeErr != nil {
				runErr = errors.Join(runErr, closeErr)
			}
		}()
		plan.loop.CloseAfterOpen = false
		plan.loop.AudioIn = audioSource
		plan.loop.MaxDuration = maxDuration
		// A finite audio source can produce an intermediate provider response
		// containing a tool call. Keep the session open through the tool result
		// and the follow-up assistant response before treating MESSAGE.END as
		// terminal.
		plan.loop.RequireAssistantResponse = true
		audioSource.bindRuntime(plan.runtime, plan.clockSource)
	}

	recording := newSessionDirectoryRecording(destination, plan, opts)
	recording.browser.start(ctx)
	plan.loop.toolLifecycleObserver = recording
	plan.loop.terminalSummaryRecorder = recording
	if plan.inferencer != nil {
		plan.inferencer = &sessionDirectoryRecordingInferencer{
			inner:     plan.inferencer,
			recording: recording,
		}
	}

	var audioOutput *sessionAudioOutput
	var audioWrapper *sessionAudioOutputInferencer
	var textOutput *sessionTextOutput
	if audioOutPath != "" {
		var sinkErr error
		audioOutput, sinkErr = newSessionAudioOutputForPlan(&plan, audioOutPath, out, nil)
		if sinkErr != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, sinkErr)
		}
		if plan.inferencer != nil {
			wirePrompt := ""
			if seed.Present {
				wirePrompt = nextSessionTextWirePrompt()
				plan.loop.Prompt = wirePrompt
			}
			audioWrapper = newSessionAudioOutputInferencer(plan.inferencer, audioOutput, wirePrompt, seed.Value)
			plan.inferencer = audioWrapper
		}
	} else if seed.Present {
		wirePrompt := nextSessionTextWirePrompt()
		plan.loop.Prompt = wirePrompt
		if plan.inferencer != nil {
			textOutput = &sessionTextOutput{writer: out}
			plan.inferencer = &sessionTextSeedInferencer{
				inner:      plan.inferencer,
				wirePrompt: wirePrompt,
				value:      seed.Value,
			}
		}
	}

	sessionOut := out
	if textOutput != nil {
		sessionOut = textOutput
	}
	if audioOutPath == "-" {
		sessionOut = io.Discard
	}
	if audioSource != nil {
		// The duration runner predates the shared AudioIn producer. Keep the
		// audio-enabled path on the loop that starts and joins that producer;
		// its MaxDuration timeout provides the same bounded session lifetime.
		runErr = plan.run(ctx, sessionOut)
	} else if maxDuration == 0 {
		runErr = plan.run(ctx, sessionOut)
	} else {
		durationCtx, durationErr := prepareSessionDurationArtifacts(ctx)
		if durationErr != nil {
			runErr = durationErr
			if audioOutput != nil {
				if closeErr := audioOutput.close(); closeErr != nil {
					runErr = errors.Join(runErr, closeErr)
				}
			}
			return finalizeSessionDirectoryRecording(runErr, recording)
		}
		durationCtx = withSessionDurationTerminalRecorder(durationCtx, recording)
		runErr = runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
	}

	if audioWrapper != nil {
		audioWrapper.wait()
		if outputErr := audioWrapper.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, outputErr))
		}
	}
	if audioOutput != nil {
		if closeErr := audioOutput.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, closeErr))
		}
	}
	return finalizeSessionDirectoryRecording(runErr, recording)
}

// finalizeSessionDirectoryRecording joins provider, cancellation, or runtime
// failures with every recording validation and persistence failure. A caller
// can therefore distinguish a failed session from a recording that was not
// published, even when both failures happen during the same shutdown.
func finalizeSessionDirectoryRecording(runErr error, recording *sessionDirectoryRecording) error {
	return errors.Join(runErr, recording.Finalize())
}

func validateSessionRecordingOptions(opts SessionRunOptions) error {
	if opts.RecordPath == "" && opts.ReplayPath == "" {
		// Directory recording is itself a valid live-session mode. Provider and
		// injected-session validation remains owned by runtime planning.
		return nil
	}
	return validateSessionRunOptions(opts)
}

func planSessionForDirectoryRecording(opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
	return planSessionForDirectoryRecordingWithInstructions(opts, "", false)
}

func planSessionForDirectoryRecordingWithInstructions(opts SessionRunOptions, systemPrompt string, withInstructions bool) (sessionRuntimePlan, func(), error) {
	planOpts := opts
	cleanup := func() {}

	var plan sessionRuntimePlan
	var err error
	if !withInstructions || (opts.ReplayPath != "" && opts.SessionInferencer == nil) {
		plan, err = planSessionRuntime(planOpts)
	} else {
		instructions, instructionErr := resolveSessionInstructions(opts, systemPrompt)
		if instructionErr != nil {
			cleanup()
			return sessionRuntimePlan{}, func() {}, instructionErr
		}
		plan, err = planSessionWithResolvedInstructions(planOpts, instructions)
	}
	if err != nil {
		cleanup()
		return sessionRuntimePlan{}, func() {}, err
	}
	if opts.RecordPath != "" && opts.SessionInferencer != nil {
		fixture := gwtesting.NewRecordingSessionInferencer(plan.inferencer)
		plan.mode = sessionRuntimeModeRecordGrok
		if strings.EqualFold(plan.provider, sessionProviderOpenAI) {
			plan.mode = sessionRuntimeModeRecordOpenAI
		}
		plan.capturePath = opts.RecordPath
		plan.inferencer = fixture
		plan.flushCapture = func() error {
			recorder := fixture.Recorder()
			if recorder == nil {
				return errors.New("session fixture recorder did not connect")
			}
			return recorder.FlushToFile(opts.RecordPath)
		}
		plan.flushCaptureTo = func(path string) error {
			recorder := fixture.Recorder()
			if recorder == nil {
				return errors.New("session fixture recorder did not connect")
			}
			return recorder.FlushToFile(path)
		}
		plan = wireSessionRecordingClaim(plan, plan.captureClaim)
	}
	return plan, cleanup, nil
}

func prepareSessionRecordingDestination(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "validate destination", path, errors.New("destination is required"))
	}
	destination := filepath.Clean(path)
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "prepare destination", destination, err)
	}

	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", recordingDestinationError(transcript.ErrRecordingDestination, "validate destination", destination, ErrSessionRecordingDirectorySymlink)
		}
		if !info.IsDir() {
			return "", recordingDestinationError(transcript.ErrRecordingDestination, "validate destination", destination, ErrSessionRecordingDirectoryNotDirectory)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return "", recordingDestinationError(transcript.ErrRecordingDestination, "inspect destination", destination, readErr)
		}
		if len(entries) != 0 {
			return "", recordingDestinationError(transcript.ErrRecordingDestinationNotEmpty, "validate destination", destination, errors.New("destination is not empty"))
		}
		parent = destination
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "inspect destination", destination, err)
	}

	probe, err := os.CreateTemp(parent, ".recording-probe-")
	if err != nil {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "probe destination", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "probe destination", destination, closeErr)
	}
	if removeErr != nil {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "remove destination probe", destination, removeErr)
	}
	return destination, nil
}

func recordingDestinationError(kind error, operation, path string, cause error) error {
	return &transcript.RecordingError{Kind: kind, Operation: operation, Path: path, Cause: cause}
}

type sessionDirectoryRecording struct {
	destination    string
	directoryClaim *sessionRecordingDirectoryClaim
	base           time.Time
	clock          *platformclock.Deterministic
	metadata       transcript.RecordingMetadata
	writeFile      transcript.RecordingWriteFile
	writeStream    transcript.RecordingWriteStream
	credentials    []string

	mu               sync.Mutex
	eventMu          sync.Mutex
	tick             uint64
	spoolDir         string
	clientSpool      *os.File
	agentSpool       *os.File
	clientPath       string
	agentPath        string
	inputPaths       []string
	outputPaths      []string
	spoolQueue       chan sessionRecordingSpoolEvent
	spoolWG          sync.WaitGroup
	spoolOverflow    bool
	spoolQueuedBytes int64
	recordErr        error
	terminal         *transcript.RecordingTerminalSummary
	conversation     sessionConversationCollector
	imageArtifacts   []transcript.RecordingArtifact
	browser          *sessionBrowserRecording

	finalizeOnce sync.Once
	finalizeErr  error
}

const sessionRecordingSpoolQueueCapacity = 256
const sessionRecordingSpoolQueueMaxBytes = 16 << 20

type sessionRecordingSpoolEvent struct {
	msg      messages.StreamMessage
	client   []byte
	agent    []byte
	audio    []byte
	outbound bool
	tick     uint64
	time     time.Time
}

var sessionRecordingClockBase = time.Unix(0, 0).UTC()

func newSessionDirectoryRecording(destination string, plan sessionRuntimePlan, opts SessionRunOptions) *sessionDirectoryRecording {
	// Recording timestamps are logical observations, not measurements of host
	// time. A fixed base makes paired captures comparable while the shared
	// deterministic clock keeps both transcript sides on the same timeline.
	base := sessionRecordingClockBase
	return &sessionDirectoryRecording{
		destination:    destination,
		directoryClaim: opts.recordingDirectoryClaim,
		base:           base,
		clock:          platformclock.NewDeterministic(base, time.Nanosecond),
		conversation:   sessionConversationCollector{now: time.Now},
		metadata: transcript.RecordingMetadata{
			Transport: "websocket",
			Model:     sessionRecordingModel(opts, plan),
			ClockBase: base.Format(time.RFC3339Nano),
			// The tick clock above is deliberately deterministic; the real
			// wall-clock start anchors bundle timing for latency analysis.
			WallClockStart: time.Now().UTC().Format(time.RFC3339Nano),
		},
		credentials: sessionRecordingCredentials(opts, plan),
		browser:     newSessionBrowserRecording(opts, plan),
	}
}

func sessionRecordingCredentials(opts SessionRunOptions, plan sessionRuntimePlan) []string {
	var credentials []string
	appendCredential := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range credentials {
			if existing == value {
				return
			}
		}
		credentials = append(credentials, value)
	}
	appendCredential(opts.APIKey)
	if opts.LoadedConfig == nil {
		return credentials
	}
	switch strings.ToLower(strings.TrimSpace(plan.provider)) {
	case sessionProviderGrok:
		if opts.LoadedConfig.Model.Grok != nil {
			appendCredential(opts.LoadedConfig.Model.Grok.APIKey)
		}
	case sessionProviderOpenAI:
		if opts.LoadedConfig.Model.OpenAI != nil {
			appendCredential(opts.LoadedConfig.Model.OpenAI.APIKey)
		}
	}
	return credentials
}

func sessionRecordingModel(opts SessionRunOptions, plan sessionRuntimePlan) string {
	if model := strings.TrimSpace(opts.Model); model != "" {
		return model
	}
	if opts.ReplayPath != "" {
		if loaded, err := gwtesting.LoadSessionCaptureForReplay(opts.ReplayPath); err == nil && loaded.Capture.Provider.Model != "" {
			return loaded.Capture.Provider.Model
		}
	}
	storage, err := config.NewDefaultConfigStorage(opts.ConfigDir)
	if err == nil {
		if loaded, loadErr := storage.Load(); loadErr == nil {
			switch strings.ToLower(plan.provider) {
			case sessionProviderGrok:
				if loaded.Model.Grok != nil && loaded.Model.Grok.Model != "" {
					return loaded.Model.Grok.Model
				}
			case sessionProviderOpenAI:
				if loaded.Model.OpenAI != nil && loaded.Model.OpenAI.Model != "" {
					return loaded.Model.OpenAI.Model
				}
			}
		}
	}
	if strings.EqualFold(plan.provider, sessionProviderOpenAI) {
		return DefaultOpenAIRealtimeModel
	}
	return "unknown"
}

type sessionDirectoryRecordingInferencer struct {
	inner     messages.SessionInferencer
	recording *sessionDirectoryRecording
}

func (i *sessionDirectoryRecordingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newSessionDirectoryRecordingSession(ctx, session, i.recording), nil
}

type sessionDirectoryRecordingSession struct {
	inner     messages.Session
	recording *sessionDirectoryRecording
	ctx       context.Context
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	once      sync.Once
}

func newSessionDirectoryRecordingSession(ctx context.Context, inner messages.Session, recording *sessionDirectoryRecording) *sessionDirectoryRecordingSession {
	capacity := inner.Receive().Cap()
	if capacity < 1024 {
		capacity = 1024
	}
	session := &sessionDirectoryRecordingSession{
		inner:     inner,
		recording: recording,
		ctx:       ctx,
		receive:   messages.NewTypedBuffer[messages.StreamMessage](capacity),
		done:      make(chan struct{}),
	}
	go session.relay()
	return session
}

func (s *sessionDirectoryRecordingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *sessionDirectoryRecordingSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *sessionDirectoryRecordingSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionDirectoryRecordingSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionDirectoryRecordingSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionDirectoryRecordingSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	payload, err := gwtesting.MarshalStreamMessage(msg)
	audio := sessionRecordingAudio(msg)
	s.recording.eventMu.Lock()
	defer s.recording.eventMu.Unlock()
	outcome := messages.SendSessionWithOutcome(ctx, s.inner, msg)
	if outcome.OK() {
		s.recording.observePayloadLocked(msg, payload, audio, err, true)
	}
	return outcome
}

// RequestResponse preserves the optional provider capability through the
// recording wrapper. Replay sessions intentionally do not expose it, so old
// captures remain valid without an unrecorded response.create expectation.
func (s *sessionDirectoryRecordingSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if !messages.SupportsSessionResponseRequests(s.inner) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (s *sessionDirectoryRecordingSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *sessionDirectoryRecordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionDirectoryRecordingSession) Done() <-chan struct{} {
	return s.inner.Done()
}

func (s *sessionDirectoryRecordingSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionDirectoryRecordingSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionDirectoryRecordingSession) Close() error {
	err := s.inner.Close()
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
	return err
}

func (s *sessionDirectoryRecordingSession) relay() {
	defer s.once.Do(func() { close(s.done) })
	source := s.inner.Receive()
	for {
		select {
		case msg := <-source.Chan():
			s.recording.observe(msg, false)
			if !s.forward(msg) {
				return
			}
		case <-s.inner.Done():
			for {
				msg, ok := source.Read()
				if !ok {
					return
				}
				s.recording.observe(msg, false)
				if !s.forward(msg) {
					return
				}
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *sessionDirectoryRecordingSession) forward(msg messages.StreamMessage) bool {
	for {
		if s.receive.Write(context.Background(), msg) {
			return true
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func (r *sessionDirectoryRecording) observe(msg messages.StreamMessage, outbound bool) {
	payload, err := gwtesting.MarshalStreamMessage(msg)
	r.observePayload(msg, payload, sessionRecordingAudio(msg), err, outbound)
}

func (r *sessionDirectoryRecording) observeToolCall(call messages.ToolCall) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.conversation.observeToolCall(call)
	r.mu.Unlock()
}

func (r *sessionDirectoryRecording) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	imageEvidence, artifactErr := r.recordCaptureArtifactLocked(call, response, failed)
	if artifactErr != nil {
		r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "validate captured image", r.destination, artifactErr))
	}
	if imageEvidence == nil {
		r.conversation.observeToolResult(call, response, failed)
	} else {
		r.conversation.observeToolResult(call, response, failed, imageEvidence)
	}
	r.mu.Unlock()
}

func (r *sessionDirectoryRecording) recordCaptureArtifactLocked(call messages.ToolCall, response messages.ToolCallResponse, failed bool) (*sessionConversationImageEvidence, error) {
	if failed {
		return nil, nil
	}
	result, err := sight.Decode([]byte(response.Content))
	if err != nil {
		// read_image and other legacy rich tools do not identify themselves as
		// captures. Only a recognized screen/page projection is a recording
		// artifact obligation.
		return nil, nil
	}
	if result.Source != sight.SourceScreen && result.Source != sight.SourceBrowserPage {
		return nil, nil
	}
	if result.Source == sight.SourceBrowserPage && (strings.TrimSpace(result.BrowserID) == "" || strings.TrimSpace(result.TargetID) == "") {
		return nil, errors.New("browser page capture omitted selected target identity")
	}
	imageParts := make([]messages.ImagePart, 0, 1)
	for _, part := range response.ContentParts {
		if imagePart, ok := part.(messages.ImagePart); ok {
			imageParts = append(imageParts, imagePart)
		}
	}
	if len(imageParts) != 1 {
		return nil, fmt.Errorf("%s capture returned %d image parts, want exactly one", call.Name, len(imageParts))
	}
	part := imageParts[0]
	if len(part.Bytes) == 0 {
		return nil, errors.New("capture image part is empty")
	}
	mediaType, _, mimeErr := mime.ParseMediaType(strings.TrimSpace(part.MediaType))
	if mimeErr != nil || strings.ToLower(strings.TrimSpace(mediaType)) != result.MIMEType {
		return nil, fmt.Errorf("capture mime type %q does not match metadata %q", part.MediaType, result.MIMEType)
	}
	digest := sha256.Sum256(part.Bytes)
	if len(part.Bytes) != result.ByteLength || hex.EncodeToString(digest[:]) != result.SHA256 {
		return nil, errors.New("capture image bytes do not match metadata digest or length")
	}
	decoded, _, decodeErr := image.Decode(bytes.NewReader(part.Bytes))
	if decodeErr != nil || decoded.Bounds().Dx() != result.Width || decoded.Bounds().Dy() != result.Height {
		if decodeErr == nil {
			decodeErr = fmt.Errorf("decoded dimensions are %dx%d, want %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy(), result.Width, result.Height)
		}
		return nil, fmt.Errorf("capture image validation failed: %w", decodeErr)
	}
	path := fmt.Sprintf("screenshots/%06d-%s.%s", len(r.imageArtifacts)+1, result.SHA256[:12], captureArtifactExtension(result.MIMEType))
	r.imageArtifacts = append(r.imageArtifacts, transcript.RecordingArtifact{
		Path:   path,
		Data:   append([]byte(nil), part.Bytes...),
		SHA256: result.SHA256,
	})
	return &sessionConversationImageEvidence{
		Path:            path,
		Source:          result.Source,
		BrowserID:       result.BrowserID,
		TargetID:        result.TargetID,
		MIMEType:        result.MIMEType,
		ByteLength:      result.ByteLength,
		Width:           result.Width,
		Height:          result.Height,
		SHA256:          result.SHA256,
		TypedProjection: result.TypedProjection,
	}, nil
}

func captureArtifactExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	default:
		return "img"
	}
}

func (r *sessionDirectoryRecording) observePayload(msg messages.StreamMessage, payload, audio []byte, err error, outbound bool) {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	r.observePayloadLocked(msg, payload, audio, err, outbound)
}

func (r *sessionDirectoryRecording) observePayloadLocked(msg messages.StreamMessage, payload, audio []byte, err error, outbound bool) {
	if err != nil {
		r.fail(recordingDestinationError(transcript.ErrRecordingWrite, "encode transcript frame", r.destination, err))
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tick = r.clock.Advance()
	timestamp := r.clock.Now()
	clientDirection := transcript.DirectionIn
	agentDirection := transcript.DirectionOut
	if outbound {
		clientDirection = transcript.DirectionOut
		agentDirection = transcript.DirectionIn
	}
	client, clientErr := transcript.Encode(transcript.NewRecord(r.tick, timestamp, transcript.PeerClient, clientDirection, transcript.StreamWebSocket, payload))
	agent, agentErr := transcript.Encode(transcript.NewRecord(r.tick, timestamp, transcript.PeerAgent, agentDirection, transcript.StreamWebSocket, payload))
	if clientErr != nil || agentErr != nil {
		if clientErr != nil {
			r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "encode client transcript", r.destination, clientErr))
		} else {
			r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "encode agent transcript", r.destination, agentErr))
		}
		return
	}
	if r.spoolOverflow {
		return
	}
	r.startSpoolWorkerLocked()
	event := sessionRecordingSpoolEvent{
		msg:      msg,
		client:   append([]byte(nil), client...),
		agent:    append([]byte(nil), agent...),
		audio:    append([]byte(nil), audio...),
		outbound: outbound,
		tick:     r.tick,
		time:     timestamp,
	}
	eventBytes := recordingSpoolEventBytes(event)
	if eventBytes > sessionRecordingSpoolQueueMaxBytes || r.spoolQueuedBytes > sessionRecordingSpoolQueueMaxBytes-eventBytes {
		r.spoolOverflow = true
		r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "enqueue recording spool", r.destination, errSessionRecordingSpoolOverflow))
		return
	}
	select {
	case r.spoolQueue <- event:
		r.spoolQueuedBytes += eventBytes
	default:
		r.spoolOverflow = true
		r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "enqueue recording spool", r.destination, errSessionRecordingSpoolOverflow))
	}
}

var errSessionRecordingSpoolOverflow = errors.New("recording spool queue is full")

func (r *sessionDirectoryRecording) startSpoolWorkerLocked() {
	if r.spoolQueue != nil {
		return
	}
	r.spoolQueue = make(chan sessionRecordingSpoolEvent, sessionRecordingSpoolQueueCapacity)
	r.spoolWG.Add(1)
	go r.runSpoolWorker(r.spoolQueue)
}

func (r *sessionDirectoryRecording) runSpoolWorker(queue <-chan sessionRecordingSpoolEvent) {
	defer r.spoolWG.Done()
	for event := range queue {
		r.mu.Lock()
		r.spoolQueuedBytes -= recordingSpoolEventBytes(event)
		r.mu.Unlock()
		r.processSpoolEvent(event)
	}
}

func recordingSpoolEventBytes(event sessionRecordingSpoolEvent) int64 {
	return int64(len(event.client)) + int64(len(event.agent)) + int64(len(event.audio))
}

func (r *sessionDirectoryRecording) processSpoolEvent(event sessionRecordingSpoolEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureSpoolLocked(); err != nil {
		r.latchRecordErrLocked(err)
		return
	}
	if err := writeRecordingSpool(r.clientSpool, event.client); err != nil {
		r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "write client transcript spool", r.destination, err))
		return
	}
	if err := writeRecordingSpool(r.agentSpool, event.agent); err != nil {
		r.latchRecordErrLocked(recordingDestinationError(transcript.ErrRecordingWrite, "write agent transcript spool", r.destination, err))
		return
	}
	if len(event.audio) > 0 {
		if event.outbound {
			path, spoolErr := r.writeAudioSpoolLocked("in", event.audio)
			if spoolErr != nil {
				r.latchRecordErrLocked(spoolErr)
				return
			}
			r.inputPaths = append(r.inputPaths, path)
			r.conversation.observe(event.msg, event.outbound, len(r.inputPaths)-1, -1)
		} else {
			path, spoolErr := r.writeAudioSpoolLocked("out", event.audio)
			if spoolErr != nil {
				r.latchRecordErrLocked(spoolErr)
				return
			}
			r.outputPaths = append(r.outputPaths, path)
			r.conversation.observe(event.msg, event.outbound, -1, len(r.outputPaths)-1)
		}
	} else {
		r.conversation.observe(event.msg, event.outbound, -1, -1)
	}
}

func (r *sessionDirectoryRecording) ensureSpoolLocked() error {
	if r.clientSpool != nil && r.agentSpool != nil {
		return nil
	}
	if r.spoolDir == "" {
		directory, err := os.MkdirTemp("", ".session-recording-spool-")
		if err != nil {
			return recordingDestinationError(transcript.ErrRecordingDestination, "create recording spool", r.destination, err)
		}
		r.spoolDir = directory
	}
	open := func(name string) (*os.File, error) {
		return os.OpenFile(filepath.Join(r.spoolDir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	}
	client, err := open("client.transcript.jsonl")
	if err != nil {
		return recordingDestinationError(transcript.ErrRecordingDestination, "create client transcript spool", r.destination, err)
	}
	agent, err := open("agent.transcript.jsonl")
	if err != nil {
		_ = client.Close()
		return recordingDestinationError(transcript.ErrRecordingDestination, "create agent transcript spool", r.destination, err)
	}
	r.clientSpool = client
	r.agentSpool = agent
	r.clientPath = filepath.Join(r.spoolDir, "client.transcript.jsonl")
	r.agentPath = filepath.Join(r.spoolDir, "agent.transcript.jsonl")
	return nil
}

func writeRecordingSpool(file *os.File, data []byte) error {
	if file == nil {
		return errors.New("recording spool is not open")
	}
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (r *sessionDirectoryRecording) writeAudioSpoolLocked(direction string, audio []byte) (string, error) {
	if err := r.ensureSpoolLocked(); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%06d.pcm", direction, len(r.inputPaths)+len(r.outputPaths))
	path := filepath.Join(r.spoolDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", recordingDestinationError(transcript.ErrRecordingDestination, "create audio spool", r.destination, err)
	}
	if err := writeRecordingSpool(file, audio); err != nil {
		_ = file.Close()
		return "", recordingDestinationError(transcript.ErrRecordingWrite, "write audio spool", r.destination, err)
	}
	if err := file.Close(); err != nil {
		return "", recordingDestinationError(transcript.ErrRecordingWrite, "close audio spool", r.destination, err)
	}
	return path, nil
}

func sessionRecordingAudio(msg messages.StreamMessage) []byte {
	audio, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || len(audio.Content) == 0 {
		return nil
	}
	return append([]byte(nil), audio.Content...)
}

func (r *sessionDirectoryRecording) fail(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.latchRecordErrLocked(err)
	r.mu.Unlock()
}

func (r *sessionDirectoryRecording) latchRecordErrLocked(err error) {
	if err != nil && r.recordErr == nil {
		r.recordErr = err
	}
}

var _ messages.SessionInferencer = (*sessionDirectoryRecordingInferencer)(nil)
var _ messages.Session = (*sessionDirectoryRecordingSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionDirectoryRecordingSession)(nil)
var _ sessionToolLifecycleObserver = (*sessionDirectoryRecording)(nil)
