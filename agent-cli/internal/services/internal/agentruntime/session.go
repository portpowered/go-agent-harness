package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimerecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// RunSession validates and runs the session inference command surface.
func RunSession(ctx context.Context, out io.Writer, opts SessionRunOptions) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() {
		closeSessionCapabilityIfNeeded(coordinator, &runErr)
	}()

	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	plan, err := planSessionRuntimeContext(ctx, opts)
	if err != nil {
		return err
	}
	return plan.run(ctx, out)
}

func openSessionLiveRecorder(opts SessionRunOptions, plan sessionRuntimePlan, destination string) (runtimesession.LiveRecorder, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, nil
	}
	service := opts.recordingService
	if service == nil {
		service = recordingwire.NewService(platformclock.Ensure(plan.clockSource))
	}
	model := strings.TrimSpace(plan.model)
	if model == "" {
		model = strings.TrimSpace(opts.Model)
	}
	options := runtimerecording.LiveEvidenceOptions{
		Destination:    destination,
		Provider:       plan.provider,
		Model:          model,
		ClockBase:      plan.clockSource.Now(),
		WallClockStart: time.Now().UTC(),
		Credentials:    sessionEvidenceCredentials(opts, plan.provider),
	}
	if opts.LoadedConfig != nil {
		browser := opts.LoadedConfig.Browser.Recording
		options.Browser = runtimerecording.BrowserRecordingOptions{
			Enabled: browser.Enabled, IncludeArguments: browser.IncludeArguments,
			IncludeResults: browser.IncludeResults, RedactURLQuery: browser.RedactURLQuery,
			RedactURLFragment: browser.RedactURLFragment,
		}
	}
	if strings.TrimSpace(opts.RecordPath) != "" {
		options.ProviderCapturePath = opts.RecordPath
	}
	return service.OpenLiveEvidence(options)
}

func startBrowserRecording(ctx context.Context, opts SessionRunOptions, recorder runtimesession.LiveRecorder) func() {
	if opts.browserRecordingStarter == nil {
		return func() {}
	}
	enabled := opts.LoadedConfig != nil && opts.LoadedConfig.Browser.Recording.Enabled
	return opts.browserRecordingStarter(ctx, enabled, opts.BrowserEventWatch, recorder)
}

func sessionEvidenceCredentials(opts SessionRunOptions, provider string) []string {
	credentials := make([]string, 0, 2)
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
		switch strings.ToLower(strings.TrimSpace(provider)) {
		case sessionProviderOpenAI:
			if opts.LoadedConfig.Model.OpenAI != nil {
				appendCredential(opts.LoadedConfig.Model.OpenAI.APIKey)
			}
		case sessionProviderGrok:
			if opts.LoadedConfig.Model.Grok != nil {
				appendCredential(opts.LoadedConfig.Model.Grok.APIKey)
			}
		}
	}
	return credentials
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
	opts.AudioInputs = scheduled
	opts.WaitForClose = true
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
	if err := sessioncontract.ValidateSessionMaxDuration(opts.MaxDuration); err != nil {
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
		opts.SessionRunOptions.Prompt, opts.SessionRunOptions.PromptProvided = opts.TextSeed.Value, true
	}
	var imageCleanup func()
	opts.SessionRunOptions, imageCleanup, err = prepareSessionImageToolAccessContext(ctx, opts.SessionRunOptions, paths, parts)
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
		defer func() { runErr = errors.Join(runErr, audioSource.Close()) }()
		opts.SessionRunOptions.ClientOwnsAudioTurnBoundaries = true
	}
	plan, wirePrompt, cleanup, err := planSessionImageRuntimeForDirectoryContext(ctx, opts.SessionRunOptions, parts, opts.TextSeed, opts.SystemPrompt, audioSource != nil || len(opts.SessionRunOptions.AudioInputs) > 0)
	if err != nil {
		return err
	}
	defer cleanup()
	if audioSource != nil {
		audioSource.bindRuntime(plan.runtime, plan.clockSource)
		plan.loop.CloseAfterOpen, plan.loop.AudioIn, plan.loop.MaxDuration = false, audioSource, opts.MaxDuration
		plan.loop.RequireAssistantResponse, plan.loop.RequireTerminalAssistantResponse = true, true
	}
	recorder, err := openSessionLiveRecorder(opts.SessionRunOptions, plan, directory)
	if err != nil {
		return err
	}
	plan.liveRecorder = recorder
	stopBrowserRecording := startBrowserRecording(ctx, opts.SessionRunOptions, recorder)
	recorderFinalized := false
	defer func() {
		stopBrowserRecording()
		if recorder != nil && !recorderFinalized {
			runErr = errors.Join(runErr, recorder.Finalize(context.WithoutCancel(ctx), runErr))
		}
	}()
	runErr = runSessionImagePlan(ctx, out, plan, opts, wirePrompt)
	recorderFinalized = true
	return runErr
}

func runSessionWithRecordingDirectory(ctx context.Context, out io.Writer, opts SessionRunOptions, directory, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, systemPrompt string, withInstructions bool, audioInput *SessionAudioInput) (runErr error) {
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
	var audioSource *sessionAudioSource
	if audioInput != nil {
		if err := validateSessionAudioInput(*audioInput); err != nil {
			return err
		}
		audioSource, err = openSessionAudioInput(*audioInput)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, audioSource.Close()) }()
		plan.loop.CloseAfterOpen, plan.loop.AudioIn, plan.loop.MaxDuration = false, audioSource, maxDuration
		plan.loop.RequireAssistantResponse = true
		audioSource.bindRuntime(plan.runtime, plan.clockSource)
	}
	recorder, err := openSessionLiveRecorder(opts, plan, directory)
	if err != nil {
		return err
	}
	plan.liveRecorder = recorder
	stopBrowserRecording := startBrowserRecording(ctx, opts, recorder)
	recorderFinalized := false
	defer func() {
		stopBrowserRecording()
		if recorder != nil && !recorderFinalized {
			runErr = errors.Join(runErr, recorder.Finalize(context.WithoutCancel(ctx), runErr))
		}
	}()
	var audioOutput *sessionAudioOutput
	var audioWrapper *sessionAudioOutputInferencer
	var textOutput *sessionTextOutput
	if audioOutPath != "" {
		audioOutput, err = newSessionAudioOutputForPlan(&plan, audioOutPath, out, nil)
		if err != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, err)
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
			return durationErr
		}
		runErr = runSessionDurationPlan(durationCtx, sessionOut, plan, maxDuration, realSessionDurationClock{})
	}
	if audioWrapper != nil {
		audioWrapper.wait()
		runErr = errors.Join(runErr, audioWrapper.err())
	}
	if audioOutput != nil {
		if closeErr := audioOutput.close(); closeErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, closeErr))
		}
	}
	recorderFinalized = true
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
	var plan sessionRuntimePlan
	var err error
	if !withInstructions || (opts.ReplayPath != "" && opts.SessionInferencer == nil) {
		plan, err = planSessionRuntimeContext(ctx, opts)
	} else {
		instructions, instructionErr := resolveSessionInstructionsContext(ctx, opts, systemPrompt)
		if instructionErr != nil {
			return sessionRuntimePlan{}, func() {}, instructionErr
		}
		plan, err = planSessionWithResolvedInstructionsContext(ctx, opts, instructions)
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
			if recorder := fixture.Recorder(); recorder != nil {
				return recorder.FlushToFile(opts.RecordPath)
			}
			return errors.New("session fixture recorder did not connect")
		}
		plan.flushCaptureTo = func(path string) error {
			if recorder := fixture.Recorder(); recorder != nil {
				return recorder.FlushToFile(path)
			}
			return errors.New("session fixture recorder did not connect")
		}
	}
	return plan, func() {}, nil
}

// sessionInstructionsInferencer decorates caller-owned session seams without
// changing their provider construction. The provider-aware runtime factory
// handles the live provider path; injected sessions receive a generic session
// update after the provider announces that the session is open.
type sessionInstructionsInferencer struct {
	inner        messages.SessionInferencer
	instructions string
	tools        []messages.ToolDefinition
}

var _ messages.SessionInferencer = (*sessionInstructionsInferencer)(nil)

func newSessionInstructionsInferencer(inner messages.SessionInferencer, instructions string, toolDefinitions []messages.ToolDefinition) messages.SessionInferencer {
	return &sessionInstructionsInferencer{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
	}
}

func cloneSessionToolDefinitions(definitions []messages.ToolDefinition) []messages.ToolDefinition {
	return messages.CanonicalToolDefinitions(definitions)
}

func (i *sessionInstructionsInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newSessionInstructionsSession(inner, ctx, i.instructions, i.tools), nil
}

type sessionInstructionsSession struct {
	inner         messages.Session
	instructions  string
	tools         []messages.ToolDefinition
	receive       *messages.TypedBuffer[messages.StreamMessage]
	ctx           context.Context
	cancel        context.CancelFunc
	configureOnce sync.Once
	done          chan struct{}
	doneOnce      sync.Once
}

var _ messages.Session = (*sessionInstructionsSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionInstructionsSession)(nil)

func newSessionInstructionsSession(inner messages.Session, parent context.Context, instructions string, toolDefinitions []messages.ToolDefinition) messages.Session {
	ctx, cancel := context.WithCancel(parent)
	session := &sessionInstructionsSession{
		inner:        inner,
		instructions: instructions,
		tools:        cloneSessionToolDefinitions(toolDefinitions),
		receive:      messages.NewTypedBuffer[messages.StreamMessage](inner.Receive().Cap()),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go session.relay()
	return session
}

func (s *sessionInstructionsSession) relay() {
	defer s.markDone()
	innerReceive := s.inner.Receive()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.inner.Done():
			s.drainAfterDone(innerReceive)
			return
		case msg := <-innerReceive.Chan():
			if !s.forward(msg) {
				return
			}
		}
	}
}

func (s *sessionInstructionsSession) drainAfterDone(innerReceive *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := innerReceive.Read()
		if !ok || !s.forward(msg) {
			return
		}
	}
}

func (s *sessionInstructionsSession) forward(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeSessionCreated {
		var configureErr error
		s.configureOnce.Do(func() {
			outcome := messages.SendSessionWithOutcome(s.ctx, s.inner, messages.StreamMessage{
				Type: messages.StreamTypeSessionUpdate,
				Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{
					Instructions: s.instructions,
					Tools:        s.tools,
				}),
			})
			if !outcome.OK() {
				configureErr = fmt.Errorf("send session instructions: %s", outcome.Status)
				if outcome.Err != nil {
					configureErr = fmt.Errorf("%w: %w", configureErr, outcome.Err)
				}
			}
		})
		if configureErr != nil {
			if closeErr := s.inner.Close(); closeErr != nil {
				configureErr = errors.Join(configureErr, fmt.Errorf("close session after instruction failure: %w", closeErr))
			}
			s.receive.Write(s.ctx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(configureErr),
			})
			return false
		}
	}
	return s.receive.Write(s.ctx, msg)
}

func (s *sessionInstructionsSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

// RequestResponse forwards the optional explicit response capability without
// changing the instruction-update lifecycle or replay behavior.
func (s *sessionInstructionsSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.inner)
}

func (s *sessionInstructionsSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

// SendMessage forwards the optional complete-message capability of the
// wrapped provider session. Instruction decoration must not hide the rich
// message path used to deliver a tool result on the next model turn.
func (s *sessionInstructionsSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse forwards deferred complete messages for callers
// that batch more than one tool result before requesting the next response.
func (s *sessionInstructionsSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionInstructionsSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.inner)
	return complete
}

func (s *sessionInstructionsSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.inner)
	return withoutResponse
}

func (s *sessionInstructionsSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *sessionInstructionsSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionInstructionsSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionInstructionsSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.inner)
}

func (s *sessionInstructionsSession) TerminalError() error {
	return terminalSessionError(s.inner)
}

func (s *sessionInstructionsSession) Close() error {
	s.cancel()
	err := s.inner.Close()
	s.markDone()
	return err
}

func (s *sessionInstructionsSession) markDone() {
	s.doneOnce.Do(func() { close(s.done) })
}
