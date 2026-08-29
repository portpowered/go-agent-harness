package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
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
	if err := ValidateSessionAudioInTurnBarge(opts.AudioInTurnBarge, len(audioPaths)); err != nil {
		return err
	}
	var coordinator *SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if len(audioPaths) == 0 {
		return RunSessionWithRecordingDirectoryAndInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, directory, audioOutPath, maxDuration, seed, systemPrompt)
	}
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
	var coordinator *SessionCapabilityCoordinator
	opts.SessionRunOptions, coordinator = prepareSessionCapabilityCoordinator(opts.SessionRunOptions)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	paths := append([]string(nil), opts.ImagePaths...)
	if len(paths) == 0 {
		return runSessionWithRecordingDirectory(ctx, out, opts.SessionRunOptions, directory, opts.AudioOutPath, opts.MaxDuration, opts.TextSeed, opts.SystemPrompt, true, audioInput)
	}
	if err := ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
		return err
	}
	if err := validateSessionRecordingOptions(opts.SessionRunOptions); err != nil {
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
		opts.SessionRunOptions.Prompt = opts.TextSeed.Value
		opts.SessionRunOptions.PromptProvided = true
	}
	destination, err := prepareSessionRecordingDestination(directory)
	if err != nil {
		return err
	}
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
	}
	plan, wirePrompt, err := planSessionImageRuntime(opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, audioSource != nil)
	if err != nil {
		return err
	}
	if audioSource != nil {
		audioSource.bindRuntime(plan.runtime)
		plan.loop.CloseAfterOpen = false
		plan.loop.AudioIn = audioSource
		plan.loop.MaxDuration = opts.MaxDuration
		// A finite image-plus-audio source can produce an intermediate provider
		// response containing a tool call. Keep the session open through the
		// tool result and the follow-up assistant response.
		plan.loop.RequireAssistantResponse = true
	}
	recording := newSessionDirectoryRecording(destination, plan, opts.SessionRunOptions)
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
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if err := validateSessionRecordingOptions(opts); err != nil {
		return err
	}

	destination, err := prepareSessionRecordingDestination(directory)
	if err != nil {
		return err
	}

	if audioInput != nil {
		// The finite source sends MESSAGE.END after its final frame. Keep the
		// provider from auto-committing the same buffer through server VAD.
		opts.ClientOwnsAudioTurnBoundaries = true
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
		audioSource.bindRuntime(plan.runtime)
	}

	recording := newSessionDirectoryRecording(destination, plan, opts)
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
		sink, sinkErr := newSessionAudioSink(audioOutPath, out)
		if sinkErr != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, sinkErr)
		}
		audioOutput = &sessionAudioOutput{sink: sink, runtime: plan.runtime}
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

// finalizeSessionDirectoryRecording preserves a provider, cancellation, or
// runtime failure when finalization cannot validate the captured recording. A
// clean run still returns any recording validation error, so an incomplete
// recording can never look successful.
func finalizeSessionDirectoryRecording(runErr error, recording *sessionDirectoryRecording) error {
	recordingErr := recording.Finalize()
	if runErr != nil && errors.Is(recordingErr, transcript.ErrInvalidRecording) {
		return runErr
	}
	return errors.Join(runErr, recordingErr)
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
	if opts.RecordPath == "" && opts.ReplayPath == "" && opts.SessionInferencer == nil {
		// The existing runtime planner uses RecordPath to select a live provider.
		// Use a private throwaway path for that selection, then disable the
		// fixture lifecycle below; --record-dir alone must not create a fixture.
		tempDir, err := os.MkdirTemp("", "agent-session-record-dir-")
		if err != nil {
			return sessionRuntimePlan{}, cleanup, fmt.Errorf("prepare session recording runtime: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(tempDir) }
		planOpts.RecordPath = filepath.Join(tempDir, "fixture.json")
	}

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
	}
	if opts.RecordPath == "" && opts.ReplayPath == "" && opts.SessionInferencer == nil {
		plan.mode = sessionRuntimeModeInjectedLive
		plan.capturePath = ""
		plan.announce = ""
		plan.flushCapture = nil
		plan.finalize = nil
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
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", recordingDestinationError(transcript.ErrRecordingDestination, "validate destination", destination, errors.New("destination must be a directory"))
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
	destination string
	base        time.Time
	clock       *platformclock.Deterministic
	metadata    transcript.RecordingMetadata
	writeFile   transcript.RecordingWriteFile

	mu           sync.Mutex
	eventMu      sync.Mutex
	tick         uint64
	client       bytes.Buffer
	agent        bytes.Buffer
	input        [][]byte
	output       [][]byte
	recordErr    error
	terminal     *transcript.RecordingTerminalSummary
	conversation sessionConversationCollector

	finalizeOnce sync.Once
	finalizeErr  error
}

var sessionRecordingClockBase = time.Unix(0, 0).UTC()

func newSessionDirectoryRecording(destination string, plan sessionRuntimePlan, opts SessionRunOptions) *sessionDirectoryRecording {
	// Recording timestamps are logical observations, not measurements of host
	// time. A fixed base makes paired captures comparable while the shared
	// deterministic clock keeps both transcript sides on the same timeline.
	base := sessionRecordingClockBase
	return &sessionDirectoryRecording{
		destination: destination,
		base:        base,
		clock:       platformclock.NewDeterministic(base, time.Nanosecond),
		metadata: transcript.RecordingMetadata{
			Transport: "websocket",
			Model:     sessionRecordingModel(opts, plan),
			ClockBase: base.Format(time.RFC3339Nano),
		},
	}
}

func sessionRecordingModel(opts SessionRunOptions, plan sessionRuntimePlan) string {
	if model := strings.TrimSpace(opts.Model); model != "" {
		return model
	}
	if opts.ReplayPath != "" {
		if capture, err := gwtesting.LoadSessionCapture(opts.ReplayPath); err == nil && capture.Provider.Model != "" {
			return capture.Provider.Model
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
			r.recordErr = recordingDestinationError(transcript.ErrRecordingWrite, "encode client transcript", r.destination, clientErr)
		} else {
			r.recordErr = recordingDestinationError(transcript.ErrRecordingWrite, "encode agent transcript", r.destination, agentErr)
		}
		return
	}
	_, _ = r.client.Write(client)
	_, _ = r.agent.Write(agent)
	if len(audio) > 0 {
		segment := append([]byte(nil), audio...)
		if outbound {
			r.input = append(r.input, segment)
			r.conversation.observe(msg, outbound, len(r.input)-1, -1)
		} else {
			r.output = append(r.output, segment)
			r.conversation.observe(msg, outbound, -1, len(r.output)-1)
		}
	} else {
		r.conversation.observe(msg, outbound, -1, -1)
	}
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
	if r.recordErr == nil {
		r.recordErr = err
	}
	r.mu.Unlock()
}

func (r *sessionDirectoryRecording) RecordTerminalSummary(summary transcript.RecordingTerminalSummary) error {
	if r == nil {
		return errors.New("nil session directory recording")
	}
	if err := summary.Validate(); err != nil {
		return err
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.setTerminalSummaryLocked(summary); err != nil {
		if r.recordErr == nil {
			r.recordErr = recordingDestinationError(transcript.ErrRecordingWrite, "capture terminal summary", r.destination, err)
		}
		return err
	}
	return nil
}

func (r *sessionDirectoryRecording) setTerminalSummaryLocked(summary transcript.RecordingTerminalSummary) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	if r.terminal == nil {
		r.terminal = &summary
		return nil
	}
	if *r.terminal == summary {
		return nil
	}
	return fmt.Errorf("conflicting terminal summary: existing=%+v received=%+v", *r.terminal, summary)
}

func (r *sessionDirectoryRecording) Finalize() error {
	r.finalizeOnce.Do(func() {
		r.eventMu.Lock()
		defer r.eventMu.Unlock()
		r.mu.Lock()
		if r.recordErr != nil {
			r.finalizeErr = r.recordErr
			r.mu.Unlock()
			return
		}
		sessionLog, sessionLogErr := sessionConversationLogJSON(&r.conversation)
		if sessionLogErr != nil {
			r.finalizeErr = recordingDestinationError(transcript.ErrRecordingWrite, "encode session log", r.destination, sessionLogErr)
			r.mu.Unlock()
			return
		}
		config := transcript.RecordingConfig{
			Destination:      r.destination,
			ClientTranscript: append([]byte(nil), r.client.Bytes()...),
			AgentTranscript:  append([]byte(nil), r.agent.Bytes()...),
			InputSegments:    copySessionRecordingSegments(r.input),
			OutputSegments:   copySessionRecordingSegments(r.output),
			SessionLog:       sessionLog,
			Metadata:         r.metadata,
			Terminal:         cloneSessionRecordingTerminalSummary(r.terminal),
			WriteFile:        r.writeFile,
		}
		r.mu.Unlock()
		r.finalizeErr = transcript.WriteRecordingBundle(config)
	})
	return r.finalizeErr
}

func cloneSessionRecordingTerminalSummary(summary *transcript.RecordingTerminalSummary) *transcript.RecordingTerminalSummary {
	if summary == nil {
		return nil
	}
	clone := *summary
	return &clone
}

func copySessionRecordingSegments(segments [][]byte) [][]byte {
	copyOf := make([][]byte, len(segments))
	for index, segment := range segments {
		copyOf[index] = append([]byte(nil), segment...)
	}
	return copyOf
}

var _ messages.SessionInferencer = (*sessionDirectoryRecordingInferencer)(nil)
var _ messages.Session = (*sessionDirectoryRecordingSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionDirectoryRecordingSession)(nil)
