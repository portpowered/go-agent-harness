package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// RunSessionWithRecordingDirectory is the directory-recording entry point for
// callers that do not need optional audio-out, prompt, or duration seams.
func RunSessionWithRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, directory string) error {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, "", 0, SessionTextSeed{}, "", false, nil)
}

func RunSessionWithRecordingDirectoryAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed) (runErr error) {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, "", false, nil)
}

func RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string) (runErr error) {
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, nil)
}

func RunSessionWithRecordingDirectoryAndInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) (runErr error) {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt)
	}
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, &input)
}

func RunSessionWithRecordingDirectoryAndInstructionsAndAudioFilesAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, audioPaths []string, systemPrompt string) (runErr error) {
	if err := sessioncontract.ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(audioPaths)); err != nil {
		return err
	}
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
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
	if strings.TrimSpace(opts.Prompt) != "" || seed.Present {
		for index := range scheduled {
			scheduled[index].AfterCompletedTurns++
		}
	}
	opts.AudioInputs, opts.WaitForClose = scheduled, true
	return runSessionWithRecordingDirectory(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt, true, nil)
}

func RunSessionWithImagesAndRecordingDirectoryAndAudioFilesAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, audioPaths []string, systemPrompt string) (runErr error) {
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
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	scheduled, err := prepareScheduledAudioInputs(audioPaths)
	if err != nil {
		return err
	}
	opts.SessionRunOptions.AudioInputs, opts.SessionRunOptions.WaitForClose = scheduled, true
	opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, opts.SystemPrompt = audioOutPath, maxDuration, seed, systemPrompt
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, nil)
}

func RunSessionWithImagesAndRecordingDirectory(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory string) (runErr error) {
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, nil)
}

func RunSessionWithImagesAndRecordingDirectoryAndAudioInput(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory string, input SessionAudioInput) (runErr error) {
	if !sessionAudioInputSelected(input) {
		return RunSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory)
	}
	return runSessionWithImagesAndRecordingDirectory(ctx, out, opts, directory, &input)
}

func runSessionWithImagesAndRecordingDirectory(ctx context.Context, out io.Writer, opts SessionImageRunOptions, directory string, audioInput *SessionAudioInput) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
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
	ownedClaim := opts.SessionRunOptions.recordingClaim == nil
	claim, err := ensureSessionRecordingClaim(&opts.SessionRunOptions)
	if err != nil {
		return err
	}
	if ownedClaim {
		defer func() { _ = claim.release() }()
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
		plan.loop.CloseAfterOpen, plan.loop.AudioIn, plan.loop.MaxDuration = false, audioSource, opts.MaxDuration
		plan.loop.RequireAssistantResponse, plan.loop.RequireTerminalAssistantResponse = true, true
	}
	return runSessionImageRecording(ctx, out, plan, opts, wirePrompt, directory)
}

func runSessionImageRecording(ctx context.Context, out io.Writer, plan sessionRuntimePlan, opts SessionImageRunOptions, wirePrompt, directory string) (runErr error) {
	recording := newSessionDirectoryRecording(directory, plan, opts.SessionRunOptions)
	if recording.openErr != nil {
		return finalizeSessionDirectoryRecording(runErr, recording)
	}
	plan.loop.toolLifecycleObserver, plan.loop.terminalSummaryRecorder = recording, recording
	if plan.inferencer != nil {
		plan.inferencer = &sessionDirectoryRecordingInferencer{inner: plan.inferencer, recording: recording}
	}
	runErr = runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
	return finalizeSessionDirectoryRecording(runErr, recording)
}

func runSessionWithRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string, withInstructions bool, audioInput *SessionAudioInput) (runErr error) {
	opts, coordinator := prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
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
	ownedClaim := opts.recordingClaim == nil
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	if ownedClaim {
		defer func() { _ = claim.release() }()
	}
	if audioInput != nil {
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
		plan.loop.CloseAfterOpen, plan.loop.AudioIn, plan.loop.MaxDuration = false, audioSource, maxDuration
		plan.loop.RequireAssistantResponse = true
		audioSource.bindRuntime(plan.runtime, plan.clockSource)
	}
	recording := newSessionDirectoryRecording(directory, plan, opts)
	if recording.openErr != nil {
		return finalizeSessionDirectoryRecording(runErr, recording)
	}
	plan.loop.toolLifecycleObserver, plan.loop.terminalSummaryRecorder = recording, recording
	if plan.inferencer != nil {
		plan.inferencer = &sessionDirectoryRecordingInferencer{inner: plan.inferencer, recording: recording}
	}
	var audioOutput *sessionAudioOutput
	var audioWrapper *sessionAudioOutputInferencer
	var textOutput *sessionTextOutput
	if audioOutPath != "" {
		audioOutput, err = newSessionAudioOutputForPlan(&plan, audioOutPath, out, nil)
		if err != nil {
			return finalizeSessionDirectoryRecording(fmt.Errorf("--audio-out %q: %w", audioOutPath, err), recording)
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
			plan.inferencer = &sessionTextSeedInferencer{inner: plan.inferencer, wirePrompt: wirePrompt, value: seed.Value}
		}
	}
	sessionOut := out
	if textOutput != nil {
		sessionOut = textOutput
	}
	if audioOutPath == "-" {
		sessionOut = io.Discard
	}
	if audioSource != nil || maxDuration == 0 {
		runErr = plan.run(ctx, sessionOut)
	} else {
		durationCtx, durationErr := prepareSessionDurationArtifacts(ctx)
		if durationErr != nil {
			return finalizeSessionDirectoryRecording(durationErr, recording)
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

func finalizeSessionDirectoryRecording(runErr error, recording *sessionDirectoryRecording) error {
	if recording == nil {
		return runErr
	}
	return errors.Join(runErr, recording.finalize(runErr))
}

func validateSessionRecordingOptions(opts SessionRunOptions) error {
	if opts.RecordPath == "" && opts.ReplayPath == "" {
		return nil
	}
	return validateSessionRunOptions(opts)
}

func planSessionForDirectoryRecording(opts SessionRunOptions) (sessionRuntimePlan, func(), error) {
	return planSessionForDirectoryRecordingWithInstructions(opts, "", false)
}

func planSessionForDirectoryRecordingWithInstructions(opts SessionRunOptions, systemPrompt string, withInstructions bool) (sessionRuntimePlan, func(), error) {
	cleanup := func() {}
	var plan sessionRuntimePlan
	var err error
	if !withInstructions || (opts.ReplayPath != "" && opts.SessionInferencer == nil) {
		plan, err = planSessionRuntime(opts)
	} else {
		instructions, instructionErr := resolveSessionInstructions(opts, systemPrompt)
		if instructionErr != nil {
			return sessionRuntimePlan{}, func() {}, instructionErr
		}
		plan, err = planSessionWithResolvedInstructions(opts, instructions)
	}
	if err != nil {
		return sessionRuntimePlan{}, func() {}, err
	}
	if opts.RecordPath != "" && opts.SessionInferencer != nil {
		fixture := gwtesting.NewRecordingSessionInferencer(plan.inferencer)
		plan.mode = sessionRuntimeModeRecordGrok
		if strings.EqualFold(plan.provider, sessionProviderOpenAI) {
			plan.mode = sessionRuntimeModeRecordOpenAI
		}
		plan.capturePath, plan.inferencer = opts.RecordPath, fixture
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

type sessionDirectoryRecording struct {
	destination string
	metadata    transcript.RecordingMetadata
	credentials []string
	session     runtimerecording.SessionRecorder
	openErr     error
	terminalMu  sync.Mutex
	terminalSet bool
}

// sessionRecordingDirectoryClaim remains as a type-only compatibility seam
// for SessionRunOptions. Directory admission is now owned by the runtime
// recording service and no CLI instance is created.
type sessionRecordingDirectoryClaim struct{}

type sessionToolLifecycleObserver interface {
	observeToolCall(messages.ToolCall)
	observeToolResult(messages.ToolCall, messages.ToolCallResponse, bool)
}

func newSessionDirectoryRecording(destination string, plan sessionRuntimePlan, opts SessionRunOptions) *sessionDirectoryRecording {
	base := time.Unix(0, 0).UTC()
	recordingClock := platformclock.NewDeterministic(base, time.Nanosecond)
	wallClockStart := recordingClock.Now()
	model := sessionRecordingModel(opts, plan)
	credentials := sessionRecordingCredentials(opts, plan)
	metadata := transcript.RecordingMetadata{Transport: "websocket", Model: model, ClockBase: base.Format(time.RFC3339Nano), WallClockStart: wallClockStart.Format(time.RFC3339Nano)}
	service := recordingwire.NewSessionService(recordingClock)
	providerCapturePath := opts.RecordPath
	if providerCapturePath == "" {
		// A replay-backed directory run already has a validated provider capture;
		// retain that input as the finalized bundle's replay source.
		providerCapturePath = opts.ReplayPath
	}
	handle, err := service.OpenSession(runtimerecording.SessionOptions{
		Destination: destination, Provider: plan.provider, Model: model, Transport: "websocket",
		ClockBase: base, WallClockStart: wallClockStart, Credentials: credentials,
		ProviderCapturePath: providerCapturePath,
		Metadata:            metadata, Browser: sessionRecordingBrowserOptions(opts, plan),
	})
	return &sessionDirectoryRecording{destination: destination, metadata: metadata, credentials: credentials, session: handle, openErr: err}
}

func sessionRecordingBrowserOptions(opts SessionRunOptions, plan sessionRuntimePlan) runtimerecording.BrowserOptions {
	if opts.LoadedConfig == nil || !opts.LoadedConfig.Browser.Recording.Enabled || opts.BrowserEventWatch == nil {
		return runtimerecording.BrowserOptions{}
	}
	settings := opts.LoadedConfig.Browser.Recording
	return runtimerecording.BrowserOptions{
		Enabled: true, IncludeArguments: settings.IncludeArguments, IncludeResults: settings.IncludeResults,
		RedactURLQuery: settings.RedactURLQuery, RedactURLFragment: settings.RedactURLFragment,
		EventSource: func(ctx context.Context) <-chan runtimerecording.BrowserEvent {
			input := opts.BrowserEventWatch(ctx)
			output := make(chan runtimerecording.BrowserEvent)
			go func() {
				defer close(output)
				for event := range input {
					select {
					case output <- convertRecordingBrowserEvent(event):
					case <-ctx.Done():
						return
					}
				}
			}()
			return output
		},
	}
}

func convertRecordingBrowserEvent(event webmcp.BrowserEvent) runtimerecording.BrowserEvent {
	tools := make([]runtimerecording.BrowserTool, 0, len(event.Tools))
	for _, tool := range event.Tools {
		tools = append(tools, runtimerecording.BrowserTool{Name: tool.Name, InputSchema: append([]byte(nil), tool.InputSchema...)})
	}
	return runtimerecording.BrowserEvent{
		Version: event.Version, Type: string(event.Type), Sequence: event.Sequence, At: event.At,
		BrowserID: string(event.BrowserID), TargetID: string(event.TargetID), FrameID: string(event.FrameID),
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration, CatalogReady: event.CatalogReady,
		ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown, ToolName: event.ToolName, Tools: tools,
		RemovedToolNames: append([]string(nil), event.RemovedToolNames...), InvocationID: string(event.InvocationID), Status: event.Status,
		Input: append([]byte(nil), event.Input...), Output: append([]byte(nil), event.Output...), ErrorCode: event.ErrorCode, Reason: event.Reason,
	}
}

func sessionRecordingCredentials(opts SessionRunOptions, plan sessionRuntimePlan) []string {
	credentials := make([]string, 0, 3)
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
	if opts.LoadedConfig != nil {
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
	if i == nil || i.recording == nil {
		return nil, errors.New("recording session is unavailable")
	}
	if i.recording.session == nil {
		return nil, i.recording.openErr
	}
	return i.recording.session.Wrap(i.inner).ConnectSession(ctx)
}

func (r *sessionDirectoryRecording) observe(message messages.StreamMessage, outbound bool) {
	if r == nil || r.session == nil {
		return
	}
	direction := runtimerecording.SessionMessageFromAgent
	if outbound {
		direction = runtimerecording.SessionMessageFromClient
	}
	_ = r.session.ObserveMessage(context.Background(), message, direction)
}

func (r *sessionDirectoryRecording) observeToolCall(call messages.ToolCall) {
	if r != nil && r.session != nil {
		_ = r.session.ObserveToolCall(context.Background(), call)
	}
}

func (r *sessionDirectoryRecording) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if r != nil && r.session != nil {
		_ = r.session.ObserveToolResult(context.Background(), call, response, failed)
	}
}

func (r *sessionDirectoryRecording) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	if r == nil || r.session == nil {
		if r == nil {
			return errors.New("nil session directory recording")
		}
		return r.openErr
	}
	err := r.session.RecordTerminalSummary(summary)
	if err == nil {
		r.terminalMu.Lock()
		r.terminalSet = true
		r.terminalMu.Unlock()
	}
	return err
}

func (r *sessionDirectoryRecording) Finalize() error { return r.finalize(nil) }

func (r *sessionDirectoryRecording) finalize(runErr error) error {
	if r == nil {
		return nil
	}
	if r.session == nil {
		return r.openErr
	}
	r.terminalMu.Lock()
	terminalSet := r.terminalSet
	r.terminalMu.Unlock()
	if !terminalSet {
		summary := transcript.RecordingTerminalSummary{
			Reason: "session_close", Classification: "completed",
			TerminalReason:     messages.TerminalReasonSessionClose,
			TerminalProvenance: messages.TerminalProvenanceSession,
			OutputState:        messages.TerminalOutputComplete,
		}
		if runErr != nil {
			summary.Reason, summary.Classification = runErr.Error(), "terminal_failure"
			summary.TerminalReason, summary.OutputState = messages.TerminalReasonTerminalFailure, messages.TerminalOutputPartial
		}
		if terminalErr := r.RecordTerminalSummary(summary); terminalErr != nil {
			runErr = errors.Join(runErr, terminalErr)
		}
	}
	return errors.Join(runErr, r.session.Finalize(context.Background(), runErr))
}

var _ messages.SessionInferencer = (*sessionDirectoryRecordingInferencer)(nil)
var _ sessionToolLifecycleObserver = (*sessionDirectoryRecording)(nil)
