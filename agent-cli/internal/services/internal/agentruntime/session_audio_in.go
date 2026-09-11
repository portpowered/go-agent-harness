package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	devicecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	audioinput "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	audioinputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	sharedclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func PrepareSessionAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	return prepareScheduledAudioInputs(paths)
}

func StartSessionAudioInterruptionsOnBrowserInvocation(parent context.Context, events <-chan webmcp.BrokerEvent, inputs []ScheduledAudioInput) (<-chan ScheduledAudioInput, func()) {
	return StartSessionAudioInterruptionsOnBrowserTool(parent, events, "", inputs)
}

func StartSessionAudioInterruptionsOnBrowserTool(parent context.Context, events <-chan webmcp.BrokerEvent, toolName string, inputs []ScheduledAudioInput) (<-chan ScheduledAudioInput, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	out := make(chan ScheduledAudioInput, len(inputs))
	cloned := cloneScheduledAudioInputs(inputs)
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				if event.Type != webmcp.BrokerEventInvocationCreated || event.State != webmcp.InvocationDispatched || event.InvocationID == "" || event.ToolName == "" || (toolName != "" && event.ToolName != toolName) {
					continue
				}
				for _, input := range cloned {
					select {
					case out <- input:
					case <-ctx.Done():
						return
					}
				}
				return
			}
		}
	}()
	return out, cancel
}

// SessionAudioInput is the CLI transport selection plus the host-provided
// source seams retained for compatibility. Runtime policy lives in audioinput.
type SessionAudioInput struct {
	Path               string
	Stdin              io.Reader
	SourceSampleRate   int
	CloseStdinOnCancel bool
	MaxDuration        time.Duration
	Source             audio.AudioSource
	SendAudioInput     func(context.Context, []byte) error
	SendEndOfTurn      func(context.Context) error
	Present            bool
	DevicePresent      bool
}

type SessionAudioInputErrorKind = audioinput.ErrorKind

const (
	SessionAudioInputEmpty      SessionAudioInputErrorKind = audioinput.KindEmpty
	SessionAudioInputMissing    SessionAudioInputErrorKind = audioinput.KindMissing
	SessionAudioInputUnreadable SessionAudioInputErrorKind = audioinput.KindUnreadable
	SessionAudioInputFormat     SessionAudioInputErrorKind = audioinput.KindFormat
	SessionAudioInputConflict   SessionAudioInputErrorKind = audioinput.KindConflict
	SessionAudioInputRead       SessionAudioInputErrorKind = audioinput.KindRead
	SessionAudioInputSend       SessionAudioInputErrorKind = audioinput.KindSend
	SessionAudioInputClose      SessionAudioInputErrorKind = audioinput.KindClose
)

var (
	ErrSessionAudioInputEmpty           = errors.New("session audio input path is empty")
	ErrSessionAudioInputMissing         = errors.New("session audio input is missing")
	ErrSessionAudioInputUnreadable      = errors.New("session audio input is unreadable")
	ErrSessionAudioInputFormat          = errors.New("session audio input format is unsupported")
	ErrSessionAudioInputConflict        = devicecontract.ErrSessionAudioInputConflict
	ErrSessionAudioInputRead            = errors.New("session audio input read failed")
	ErrSessionAudioInputSend            = errors.New("session audio input send failed")
	ErrSessionAudioInputClose           = errors.New("session audio input close failed")
	ErrSessionAudioInputUninterruptible = errors.New("session audio input reader cannot be interrupted safely")
	ErrSessionAudioInputEndOfTurnLost   = errors.New("end-of-turn signal was not delivered before session shutdown")
	ErrSessionAudioPCM16Truncated       = errors.New("session PCM16 audio has a truncated sample")
)

type SessionAudioInputError struct {
	Kind SessionAudioInputErrorKind
	Path string
	Err  error
}

func (e *SessionAudioInputError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("agent session --audio-in %q: %s", e.Path, e.Kind)
	}
	return fmt.Sprintf("agent session --audio-in %q: %s: %v", e.Path, e.Kind, e.Err)
}

func (e *SessionAudioInputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(sessionAudioInputKindError(e.Kind), e.Err)
}

func sessionAudioInputKindError(kind SessionAudioInputErrorKind) error {
	switch kind {
	case SessionAudioInputEmpty:
		return ErrSessionAudioInputEmpty
	case SessionAudioInputMissing:
		return ErrSessionAudioInputMissing
	case SessionAudioInputUnreadable:
		return ErrSessionAudioInputUnreadable
	case SessionAudioInputFormat:
		return ErrSessionAudioInputFormat
	case SessionAudioInputConflict:
		return ErrSessionAudioInputConflict
	case SessionAudioInputRead:
		return ErrSessionAudioInputRead
	case SessionAudioInputSend:
		return ErrSessionAudioInputSend
	case SessionAudioInputClose:
		return ErrSessionAudioInputClose
	default:
		return nil
	}
}

func emptySessionAudioInput(path string) error {
	return &SessionAudioInputError{Kind: SessionAudioInputEmpty, Path: path, Err: fmt.Errorf("no audio frames were sent; refusing to commit an empty user turn: %w", ErrSessionAudioInputEmpty)}
}

func RunSessionWithAudioInput(ctx context.Context, out io.Writer, opts SessionRunOptions, input SessionAudioInput) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if !sessionAudioInputSelected(input) {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	opts.ClientOwnsAudioTurnBoundaries = true
	return runSessionWithAudioInputPlan(ctx, out, input, "", SessionTextSeed{}, func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionRuntime(opts)
	})
}

func RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) error {
	return RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx, out, opts, "", maxDuration, seed, input, systemPrompt)
}

func RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if err := sessioncontract.ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !sessionAudioInputSelected(input) {
		return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, audioOutPath, maxDuration, seed, systemPrompt)
	}
	if audioOutPath != "" {
		opts.AudioOutputRequested = true
	}
	if seed.Present {
		opts.Prompt, opts.PromptProvided = seed.Value, true
	}
	input.MaxDuration = maxDuration
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if err := validateSessionAudioInputFileExists(input); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { _ = claim.release() }()
	opts.ClientOwnsAudioTurnBoundaries = true
	factory := func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		if opts.ReplayPath != "" && (opts.SessionInferencer == nil || strings.TrimSpace(systemPrompt) == "") {
			return planSessionRuntime(opts)
		}
		instructions, err := resolveSessionInstructions(opts, systemPrompt)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionWithResolvedInstructions(opts, instructions)
	}
	return runSessionWithAudioInputPlan(ctx, out, input, audioOutPath, seed, factory)
}

func sessionAudioInputSelected(input SessionAudioInput) bool {
	return input.Present || input.Path != ""
}

func runSessionWithAudioInputPlan(ctx context.Context, out io.Writer, input SessionAudioInput, audioOutPath string, seed SessionTextSeed, planFactory func() (sessionRuntimePlan, error)) (runErr error) {
	source, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()
	plan, err := planFactory()
	if err != nil {
		return err
	}
	source.bindRuntime(plan.runtime, plan.clockSource)
	source.bindProviderRate(plan.inputAudioSampleRate)
	if input.MaxDuration > 0 {
		plan.loop.MaxDuration = input.MaxDuration
	}
	sessionOut := out
	var audioOutput *sessionAudioOutput
	var audioWrapped *sessionAudioOutputInferencer
	if audioOutPath != "" {
		audioOutput, err = newSessionAudioOutputForPlan(&plan, audioOutPath, out, nil)
		if err != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, err)
		}
		defer func() {
			if closeErr := audioOutput.close(); closeErr != nil {
				runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", audioOutPath, closeErr))
			}
		}()
		audioWrapped = newSessionAudioOutputInferencer(plan.inferencer, audioOutput, "", seed.Value)
		plan.inferencer = audioWrapped
		plan.loop.AudioOutputError = func() error {
			audioWrapped.wait()
			return joinSessionAudioOutputError(nil, audioOutPath, audioWrapped.err())
		}
		if audioOutPath == "-" {
			sessionOut = io.Discard
		}
	}
	plan.loop.CloseAfterOpen = false
	plan.loop.RequireAssistantResponse = true
	plan.loop.AudioIn = source
	runErr = plan.run(ctx, sessionOut)
	if audioWrapped != nil {
		audioWrapped.wait()
		runErr = joinSessionAudioOutputError(runErr, audioOutPath, audioWrapped.err())
	}
	return runErr
}

func validateSessionAudioInput(input SessionAudioInput) error {
	if input.DevicePresent {
		return &SessionAudioInputError{Kind: SessionAudioInputConflict, Path: input.Path, Err: ErrSessionAudioInputConflict}
	}
	if strings.TrimSpace(input.Path) == "" {
		return &SessionAudioInputError{Kind: SessionAudioInputEmpty, Path: input.Path, Err: ErrSessionAudioInputEmpty}
	}
	return nil
}

func validateSessionAudioInputFileExists(input SessionAudioInput) error {
	if input.Source != nil || input.Path == "" || input.Path == "-" {
		return nil
	}
	if _, err := os.Stat(input.Path); err != nil {
		return classifySessionAudioOpenError(input.Path, err)
	}
	return nil
}

func openSessionAudioInput(input SessionAudioInput) (*sessionAudioSource, error) {
	rate := input.SourceSampleRate
	if rate == 0 {
		rate = audio.SampleRate
	}
	if input.Source != nil {
		return &sessionAudioSource{source: input.Source, path: input.Path, sourceRate: rate, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
	}
	if strings.EqualFold(filepath.Ext(input.Path), ".wav") {
		source, err := openSessionWAVSource(input.Path)
		if err != nil {
			return nil, err
		}
		return &sessionAudioSource{source: source, path: input.Path, sourceRate: sessionAudioSourceSampleRate(source, audio.SampleRate), paced: true, send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
	}
	stdin := input.Stdin
	var reader *sessionAudioReader
	var owned *os.File
	if input.Path == "-" {
		if stdin == nil {
			return nil, classifySessionAudioOpenError(input.Path, audio.ErrNilStream)
		}
		if file, ok := stdin.(*os.File); ok && input.CloseStdinOnCancel {
			var err error
			owned, err = devicegateway.OpenInterruptibleInput(file)
			if err != nil {
				return nil, classifySessionAudioOpenError(input.Path, err)
			}
			stdin = owned
		}
		reader = newSessionAudioReader(stdin, input.CloseStdinOnCancel)
		stdin = reader
	}
	source, err := audio.NewFileSource(input.Path, stdin)
	if err != nil {
		if owned != nil {
			_ = owned.Close()
		}
		return nil, classifySessionAudioOpenError(input.Path, err)
	}
	if input.Path != "-" {
		info, statErr := os.Stat(input.Path)
		if statErr != nil {
			_ = source.Close()
			return nil, classifySessionAudioOpenError(input.Path, statErr)
		}
		if info.IsDir() {
			_ = source.Close()
			return nil, &SessionAudioInputError{Kind: SessionAudioInputUnreadable, Path: input.Path, Err: fmt.Errorf("path is a directory; provide a .wav, .pcm, or .raw file")}
		}
	}
	return &sessionAudioSource{source: source, path: input.Path, reader: reader, ownedInput: owned, paced: input.Path != "-", send: input.SendAudioInput, endOfTurn: input.SendEndOfTurn}, nil
}

func prepareScheduledAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	inputs := make([]ScheduledAudioInput, 0, len(paths))
	for index, path := range paths {
		input := SessionAudioInput{Path: path, Present: true}
		if err := validateSessionAudioInput(input); err != nil {
			return nil, err
		}
		pcm, rate, err := readSessionAudioInputPCM(input)
		if err != nil {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, err)
		}
		if len(pcm) == 0 {
			return nil, fmt.Errorf("load audio turn %d from %q: %w", index+1, path, emptySessionAudioInput(path))
		}
		inputs = append(inputs, ScheduledAudioInput{AfterCompletedTurns: index, PCM: pcm, SourceSampleRate: rate, EndOfTurn: true})
	}
	return inputs, nil
}

func readSessionAudioInputPCM(input SessionAudioInput) (pcm []byte, sourceRate int, runErr error) {
	source, err := openSessionAudioInput(input)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()
	pcm, sourceRate, err = audioInputService(nil).Read(context.Background(), audioinput.Input{Path: input.Path, Source: source.source, SourceSampleRate: source.sourceRate})
	if err != nil {
		return nil, 0, adaptSessionAudioError(err, input.Path)
	}
	return pcm, sourceRate, nil
}

func classifySessionAudioOpenError(path string, err error) error {
	kind := SessionAudioInputUnreadable
	switch {
	case errors.Is(err, audio.ErrUnsupportedFormat):
		kind = SessionAudioInputFormat
	case errors.Is(err, os.ErrNotExist):
		kind = SessionAudioInputMissing
	case errors.Is(err, audio.ErrNilStream):
		kind = SessionAudioInputUnreadable
	}
	return &SessionAudioInputError{Kind: kind, Path: path, Err: err}
}

type sessionAudioSource struct {
	source       audio.AudioSource
	path         string
	sourceRate   int
	providerRate int
	reader       *sessionAudioReader
	ownedInput   *os.File
	paced        bool
	send         func(context.Context, []byte) error
	endOfTurn    func(context.Context) error
	runtime      *sessionRuntimeObservationRecorder
	clock        sharedclock.Source
	once         sync.Once
	err          error
}

func (s *sessionAudioSource) bindProviderRate(rate int) {
	if s != nil {
		s.providerRate = rate
	}
}
func (s *sessionAudioSource) bindContext(ctx context.Context) {
	if s != nil && s.reader != nil {
		s.reader.bindContext(ctx)
	}
}
func (s *sessionAudioSource) bindRuntime(runtime *sessionRuntimeObservationRecorder, source sharedclock.Source) {
	if s != nil {
		s.runtime, s.clock = runtime, source
	}
}
func (s *sessionAudioSource) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.source != nil {
			s.err = s.source.Close()
		}
		if s.ownedInput != nil && s.reader != nil {
			s.err = errors.Join(s.err, s.reader.Close())
		}
	})
	if s.err == nil {
		return nil
	}
	return &SessionAudioInputError{Kind: SessionAudioInputClose, Path: s.path, Err: s.err}
}

type sessionAudioReader struct {
	*audioinput.ReaderSource
	reader        io.Reader
	closeOnCancel bool
}

func newSessionAudioReader(reader io.Reader, closeOnCancel bool) *sessionAudioReader {
	inner, _ := audioinput.NewReaderSource(reader, closeOnCancel)
	return &sessionAudioReader{ReaderSource: inner, reader: reader, closeOnCancel: closeOnCancel}
}
func (r *sessionAudioReader) bindContext(ctx context.Context) {
	if r != nil && r.ReaderSource != nil {
		r.ReaderSource.BindContext(ctx)
	}
}
func (r *sessionAudioReader) Read(p []byte) (int, error) {
	if r == nil || r.ReaderSource == nil {
		return 0, io.EOF
	}
	return r.ReaderSource.Read(p)
}
func (r *sessionAudioReader) Close() error {
	if r == nil || r.ReaderSource == nil {
		return nil
	}
	return r.ReaderSource.Close()
}

func streamSessionAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource) (runErr error) {
	if source == nil {
		return &SessionAudioInputError{Kind: SessionAudioInputUnreadable, Err: audio.ErrNilStream}
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()
	var observer audioinput.Observer
	if source.runtime != nil {
		observer = audioinput.ObserverFunc(func(o audioinput.AudioObservation) { source.runtime.audioInput(o.PCM) })
	}
	streamClock := sharedclock.Ensure(source.clock)
	err := audioInputService(streamClock).Stream(ctx, audioinput.Input{Path: source.path, Source: source.source, SourceSampleRate: source.sourceRate, ProviderSampleRate: source.providerRate, Pace: source.paced, Clock: streamClock, SendAudioInput: source.send, SendEndOfTurn: source.endOfTurn, Observer: observer}, loop)
	if err == nil {
		return nil
	}
	return adaptSessionAudioError(err, source.path)
}

func sendEventDrivenAudioInput(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, input ScheduledAudioInput) error {
	var observer audioinput.Observer
	if opts.observer != nil {
		observer = audioinput.ObserverFunc(func(o audioinput.AudioObservation) { opts.observer.accountInputAudio(len(o.PCM)) })
	}
	err := audioInputService(nil).Dispatch(ctx, loop, runtimeScheduledAudioInput(input), audioinput.DispatchOptions{ProviderSampleRate: opts.InputAudioSampleRate, Observer: observer})
	if err != nil {
		return fmt.Errorf("send event-driven audio input: %w", adaptSessionAudioError(err, ""))
	}
	if input.EndOfTurn && opts.observer != nil {
		opts.observer.armProviderProgress()
	}
	return nil
}

// dispatchScheduledAudioInput is the small CLI scheduling adapter; conversion,
// send ordering, cloning, observation, and end-of-turn belong to runtime.
func dispatchScheduledAudioInput(ctx context.Context, loop scheduledSessionInputSender, input ScheduledAudioInput, observer *sessionProgressObserver) error {
	var audioObserver audioinput.Observer
	if observer != nil {
		audioObserver = audioinput.ObserverFunc(func(o audioinput.AudioObservation) { observer.accountInputAudio(len(o.PCM)) })
	}
	err := audioInputService(nil).Dispatch(ctx, loop, runtimeScheduledAudioInput(input), audioinput.DispatchOptions{ProviderSampleRate: input.SourceSampleRate, Observer: audioObserver})
	if err != nil {
		return adaptSessionAudioError(err, "")
	}
	if input.EndOfTurn && observer != nil {
		observer.armProviderProgress()
	}
	return nil
}

func (o *sessionProgressObserver) accountInputAudio(n int) {
	if o != nil && n > 0 {
		o.account(metrics.DirectionInput, metrics.ModalityAudio, n)
	}
}

func audioInputService(source sharedclock.Source) audioinput.Service {
	return audioinputwire.NewService(source)
}

func runtimeScheduledAudioInput(input ScheduledAudioInput) audioinput.ScheduledInput {
	return audioinput.ScheduledInput{AfterCompletedTurns: input.AfterCompletedTurns, PCM: input.PCM, SourceSampleRate: input.SourceSampleRate, EndOfTurn: input.EndOfTurn}
}

func adaptSessionAudioError(err error, path string) error {
	if err == nil {
		return nil
	}
	var typed *audioinput.Error
	if !errors.As(err, &typed) {
		return adaptSessionAudioCause(err)
	}
	kind := SessionAudioInputErrorKind(typed.Kind)
	if kind == audioinput.KindUninterruptible {
		kind = SessionAudioInputRead
	}
	if path == "" {
		path = typed.Path
	}
	return &SessionAudioInputError{Kind: kind, Path: path, Err: adaptSessionAudioCause(typed.Err)}
}

func adaptSessionAudioCause(err error) error {
	if err == nil {
		return nil
	}
	var causes []error
	if errors.Is(err, audioinput.ErrEndOfTurnLost) {
		causes = append(causes, ErrSessionAudioInputEndOfTurnLost)
	}
	if errors.Is(err, audioinput.ErrUninterruptible) {
		causes = append(causes, ErrSessionAudioInputUninterruptible)
	}
	if errors.Is(err, audioinput.ErrPCM16Truncated) {
		causes = append(causes, ErrSessionAudioPCM16Truncated)
	}
	if len(causes) == 0 {
		return err
	}
	causes = append(causes, err)
	return errors.Join(causes...)
}

func joinSessionTerminationErrors(runErr, audioErr error) error {
	var errs []error
	if runErr != nil && !isSessionCancellation(runErr) {
		errs = append(errs, fmt.Errorf("session error: %w", runErr))
	}
	if audioErr != nil && !isSessionCancellation(audioErr) {
		errs = append(errs, audioErr)
	}
	return errors.Join(errs...)
}
func isSessionCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func shouldStopAudioInputSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent, awaitingResponse bool) bool {
	if !awaitingResponse {
		return msg.Type == messages.StreamTypeSessionClose
	}
	if msg.Type == messages.StreamTypeMessageEnd && opts.observer != nil && (opts.observer.hasTerminalToolContinuationFailure() || opts.observer.hasTerminalScheduledResponseFailure()) {
		return true
	}
	if opts.WaitForClose {
		return isTerminalErrorMessage(msg) || msg.Type == messages.StreamTypeSessionClose
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if opts.observer != nil && !opts.observer.lastMessageEndAdmitted() {
			return false
		}
		if opts.RequireAssistantResponse && (msg.Role == messages.RoleTool || opts.observer == nil || !opts.observer.assistantResponseCompleted()) {
			return false
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return isTerminalErrorMessage(msg)
	}
}

type sessionAudioRateSource interface{ SampleRate() int }

func sessionAudioSourceSampleRate(source audio.AudioSource, fallback int) int {
	if rated, ok := source.(sessionAudioRateSource); ok && rated.SampleRate() > 0 {
		return rated.SampleRate()
	}
	return fallback
}

func openSessionWAVSource(path string) (audio.AudioSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, classifySessionAudioOpenError(path, err)
	}
	source, err := newSessionWAVSource(path, file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return source, nil
}
func newSessionWAVSource(path string, r io.ReadSeekCloser) (audio.AudioSource, error) {
	source, err := audioinput.NewWAVSource(path, r)
	if err != nil {
		return nil, adaptSessionAudioError(err, path)
	}
	return source, nil
}

const sessionRealtimeAudioSampleRate = int(models.SampleRate24000)

var ErrSessionAudioSampleRateConflict = errors.New("session input and output sample rates conflict")

type sessionAudioOutputConfigurer interface {
	SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
}
type sessionAudioInputConfigurer interface {
	SetSessionAudioInput(models.AudioFormat, models.SampleRate)
}
type sessionAudioRequestProvider interface {
	Request() inference.SessionRequest
}

func resolveSessionAudioSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) (int, error) {
	inRate, outRate := plan.inputAudioSampleRate, plan.outputAudioSampleRate
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		config := requested.Request().Config
		if inRate <= 0 {
			inRate = int(config.InputAudioSampleRate)
		}
		if outRate <= 0 {
			outRate = int(config.OutputAudioSampleRate)
		}
	}
	if inRate > 0 && outRate > 0 && inRate != outRate {
		return 0, fmt.Errorf("%w: input=%d Hz output=%d Hz", ErrSessionAudioSampleRateConflict, inRate, outRate)
	}
	if inRate > 0 {
		return inRate, nil
	}
	if outRate > 0 {
		return outRate, nil
	}
	if opts.ReplayPath == "" && (plan.provider == sessionProviderOpenAI || plan.provider == sessionProviderGrok) {
		return sessionRealtimeAudioSampleRate, nil
	}
	return audio.SampleRate, nil
}

func configureSessionAudioContract(opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if plan == nil {
		return nil
	}
	rate, err := resolveSessionAudioSampleRate(opts, *plan)
	if err != nil {
		return err
	}
	plan.outputAudioSampleRate, plan.inputAudioSampleRate = rate, rate
	if configurer, ok := plan.inferencer.(sessionAudioOutputConfigurer); ok {
		configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rate))
	}
	if configurer, ok := plan.inferencer.(sessionAudioInputConfigurer); ok {
		configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(rate))
	}
	return nil
}

func convertSessionAudioPCM(pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	converted, err := audioInputService(nil).ConvertPCM(pcm, sourceRate, providerRate)
	if err != nil {
		return nil, adaptSessionAudioCause(err)
	}
	return converted, nil
}
func convertScheduledAudioInputs(inputs []ScheduledAudioInput, providerRate int) ([]ScheduledAudioInput, error) {
	runtimeInputs := make([]audioinput.ScheduledInput, len(inputs))
	for index, input := range inputs {
		runtimeInputs[index] = runtimeScheduledAudioInput(input)
	}
	converted, err := audioInputService(nil).ConvertScheduled(runtimeInputs, providerRate)
	if err != nil {
		return nil, adaptSessionAudioCause(err)
	}
	result := make([]ScheduledAudioInput, len(converted))
	for index, input := range converted {
		result[index] = ScheduledAudioInput{AfterCompletedTurns: input.AfterCompletedTurns, PCM: input.PCM, SourceSampleRate: input.SourceSampleRate, EndOfTurn: input.EndOfTurn}
	}
	return result, nil
}
