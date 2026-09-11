package agentruntime

import (
	"context"
	"errors"
	"fmt"
	sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	devicecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	audioinput "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	audioinputwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"io"
	"os"
	"strings"
	"time"
)

func PrepareSessionAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	return prepareScheduledAudioInputs(paths)
}
func StartSessionAudioInterruptionsOnBrowserInvocation(parent context.Context, events <-chan webmcp.BrokerEvent, inputs []ScheduledAudioInput) (<-chan ScheduledAudioInput, func()) {
	return StartSessionAudioInterruptionsOnBrowserTool(parent, events, "", inputs)
}
func StartSessionAudioInterruptionsOnBrowserTool(parent context.Context, events <-chan webmcp.BrokerEvent, toolName string, inputs []ScheduledAudioInput) (<-chan ScheduledAudioInput, func()) {
	ctxParent := parent //nolint:contextcheck // normalize the legacy nil-parent API before deriving the cancellable child context.
	if ctxParent == nil {
		ctxParent = context.Background()
	}
	ctx, cancel := context.WithCancel(ctxParent)
	mapped := make(chan audioinput.InvocationEvent, 1)
	go func() {
		defer close(mapped)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				mappedEvent := audioinput.InvocationEvent{Dispatched: event.Type == webmcp.BrokerEventInvocationCreated && event.State == webmcp.InvocationDispatched, InvocationID: string(event.InvocationID), ToolName: event.ToolName}
				select {
				case mapped <- mappedEvent:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	out, stop := audioinputwire.NewService(nil).ReleaseOnInvocation(ctx, mapped, inputs, toolName)
	return out, func() { cancel(); stop() }
}

var ErrSessionAudioInputConflict = devicecontract.ErrSessionAudioInputConflict

const sessionRealtimeAudioSampleRate = int(models.SampleRate24000)

func RunSessionWithAudioInput(ctx context.Context, out io.Writer, opts SessionRunOptions, input SessionAudioInput) (runErr error) {
	var coordinator SessionCapabilityCoordinator
	opts, coordinator = prepareSessionCapabilityCoordinator(opts)
	defer func() { closeSessionCapabilityIfNeeded(coordinator, &runErr) }()
	if !sessionAudioInputSelected(input) {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}
	claim, err := ensureSessionRecordingClaim(&opts)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, claim.release()) }()
	opts.ClientOwnsAudioTurnBoundaries = true
	return runSessionWithAudioInputPlan(ctx, out, input, "", SessionTextSeed{}, func() (sessionRuntimePlan, error) {
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
	input, claim, err := prepareInstructionAudioSession(&opts, audioOutPath, maxDuration, seed, input)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, claim.release()) }()
	opts.ClientOwnsAudioTurnBoundaries = true
	return runSessionWithAudioInputPlan(ctx, out, input, audioOutPath, seed, func() (sessionRuntimePlan, error) {
		if opts.ReplayPath != "" && (opts.SessionInferencer == nil || strings.TrimSpace(systemPrompt) == "") {
			return planSessionRuntime(opts)
		}
		instructions, err := resolveSessionInstructions(opts, systemPrompt)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionWithResolvedInstructions(opts, instructions)
	})
}
func prepareInstructionAudioSession(opts *SessionRunOptions, audioOutPath string, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput) (SessionAudioInput, *sessionRecordingClaim, error) {
	if audioOutPath != "" {
		opts.AudioOutputRequested = true
	}
	if seed.Present {
		opts.Prompt, opts.PromptProvided = seed.Value, true
	}
	input.MaxDuration = maxDuration
	if err := validateSessionAudioInput(input); err != nil {
		return input, nil, err
	}
	if err := validateSessionRunOptions(*opts); err != nil {
		return input, nil, err
	}
	claim, err := ensureSessionRecordingClaim(opts)
	return input, claim, err
}
func runSessionWithAudioInputPlan(ctx context.Context, out io.Writer, input SessionAudioInput, audioOutPath string, seed SessionTextSeed, planFactory func() (sessionRuntimePlan, error)) (runErr error) {
	source, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, source.Close()) }()
	plan, err := planFactory()
	if err != nil {
		return err
	}
	source.bindRuntime(plan.runtime, plan.clockSource)
	source.ProviderSampleRate = plan.inputAudioSampleRate
	if input.MaxDuration > 0 {
		plan.loop.MaxDuration = input.MaxDuration
	}
	sessionOut, audioWrapped, closeAudio, err := configureSessionAudioOutput(&plan, audioOutPath, out, seed.Value)
	if err != nil {
		return err
	}
	if closeAudio != nil {
		defer func() { runErr = errors.Join(runErr, closeAudio()) }()
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
func configureSessionAudioOutput(plan *sessionRuntimePlan, audioOutPath string, out io.Writer, seed string) (io.Writer, *sessionAudioOutputInferencer, func() error, error) {
	if audioOutPath == "" {
		return out, nil, nil, nil
	}
	audioOutput, err := newSessionAudioOutputForPlan(plan, audioOutPath, out, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("--audio-out %q: %w", audioOutPath, err)
	}
	audioWrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOutput, "", seed)
	plan.inferencer = audioWrapped
	plan.loop.AudioOutputError = func() error {
		audioWrapped.wait()
		return joinSessionAudioOutputError(nil, audioOutPath, audioWrapped.err())
	}
	sessionOut := out
	if audioOutPath == "-" {
		sessionOut = io.Discard
	}
	return sessionOut, audioWrapped, func() error {
		if err := audioOutput.close(); err != nil {
			return fmt.Errorf("--audio-out %q: %w", audioOutPath, err)
		}
		return nil
	}, nil
}
func validateSessionAudioInput(input SessionAudioInput) error {
	return audioinputwire.NewService(nil).ValidateSpec(input, ErrSessionAudioInputConflict)
}
func openSessionAudioInput(input SessionAudioInput) (*sessionAudioSource, error) {
	service := audioinputwire.NewService(nil)
	if err := service.ValidateSpec(input, ErrSessionAudioInputConflict); err != nil {
		return nil, err
	}
	managed, err := service.Open(input, audioinput.Opener{
		PrepareStdin: func(stdin io.Reader, closeOnCancel bool) (io.Reader, error) {
			if file, ok := stdin.(*os.File); ok && closeOnCancel {
				opened, err := devicegateway.OpenInterruptibleInput(file)
				if err != nil {
					return nil, err
				}
				stdin = opened
			}
			return stdin, nil
		},
		OpenWAV:   openSessionWAVSource,
		OpenFile:  func(path string, input io.Reader) (audio.AudioSource, error) { return audio.NewFileSource(path, input) },
		CheckPath: os.Stat,
		Classify:  service.ClassifyOpenError,
	})
	if err != nil {
		return nil, err
	}
	return newSessionAudioSource(managed), nil
}
func prepareScheduledAudioInputs(paths []string) ([]ScheduledAudioInput, error) {
	return audioinputwire.NewService(nil).PrepareScheduled(paths, readSessionAudioInputPCM)
}
func readSessionAudioInputPCM(path string) (pcm []byte, sourceRate int, runErr error) {
	input := SessionAudioInput{Path: path, Present: true}
	source, err := openSessionAudioInput(input)
	if err != nil {
		return nil, 0, err
	}
	defer func() { runErr = adaptSessionAudioError(errors.Join(runErr, source.Close()), input.Path) }()
	return audioinputwire.NewService(nil).Read(context.Background(), source.Input(nil))
}
func sessionAudioInputSelected(input SessionAudioInput) bool { return input.Selected() }

type sessionAudioSource struct {
	*audioinput.ManagedSource
	reader *struct{ closeOnCancel bool }
	send   func(context.Context, []byte) error
}

func newSessionAudioSource(managed *audioinput.ManagedSource) *sessionAudioSource {
	return &sessionAudioSource{ManagedSource: managed, reader: &struct{ closeOnCancel bool }{closeOnCancel: managed.Reader == nil || managed.CloseOnCancel}, send: managed.SendAudioInput}
}
func (s *sessionAudioSource) bindContext(ctx context.Context)    { s.ManagedSource.BindContext(ctx) }
func (s *sessionAudioSource) bindRuntime(r *rec, c clock.Source) { s.BindObserver(c, r.audioInput) }
func streamSessionAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource) (runErr error) {
	return adaptSessionAudioError(errors.Join(audioinputwire.NewService(source.ClockSource()).Stream(ctx, source.Input(source.ClockSource()), loop), source.Close()), source.Path)
}
func sendEventDrivenAudioInput(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, input ScheduledAudioInput) error {
	err := audioinputwire.NewService(nil).Dispatch(ctx, loop, input, audioinput.DispatchOptions{ProviderSampleRate: opts.InputAudioSampleRate, Observer: opts.observer})
	if err != nil {
		return fmt.Errorf("send event-driven audio input: %w", adaptSessionAudioError(err, ""))
	}
	if input.EndOfTurn && opts.observer != nil {
		opts.observer.armProviderProgress()
	}
	return nil
}
func (o *sessionProgressObserver) ObserveAudioInput(observation audioinput.AudioObservation) {
	if o != nil && len(observation.PCM) > 0 {
		o.account(metrics.DirectionInput, metrics.ModalityAudio, len(observation.PCM))
	}
}
func adaptSessionAudioError(err error, path string) error {
	return audioinputwire.NewService(nil).AdaptError(err, path, audioinput.KindRead, ErrSessionAudioInputConflict)
}
func joinSessionTerminationErrors(runErr, audioErr error) error {
	return audioinputwire.NewService(nil).JoinTerminationErrors(runErr, audioErr)
}
func isSessionCancellation(err error) bool {
	return audioinputwire.NewService(nil).IsExpectedCancellation(err)
}
func shouldStopAudioInputSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, _ bool, awaitingResponse bool) bool {
	return audioinputwire.NewService(nil).ShouldStop(msg, audioinput.TurnStopPolicy{AwaitingResponse: awaitingResponse, WaitForClose: opts.WaitForClose, RequireAssistantResponse: opts.RequireAssistantResponse, Terminal: isTerminalErrorMessage, MessageEndAdmitted: func() bool { return opts.observer == nil || opts.observer.lastMessageEndAdmitted() }, TerminalToolFailure: func() bool { return opts.observer != nil && opts.observer.hasTerminalToolContinuationFailure() }, TerminalScheduledFailure: func() bool { return opts.observer != nil && opts.observer.hasTerminalScheduledResponseFailure() }, AssistantResponseCompleted: func() bool { return opts.observer != nil && opts.observer.assistantResponseCompleted() }})
}
func openSessionWAVSource(path string) (audio.AudioSource, error) {
	service := audioinputwire.NewService(nil)
	reader, err := os.Open(path)
	if err != nil {
		return nil, service.ClassifyOpenError(path, err)
	}
	source, err := audio.NewWAVSource(path, reader)
	if err != nil {
		formatErr := service.ClassifyOpenError(path, errors.Join(audioinput.ErrFormat, audio.ErrUnsupportedFormat, err))
		if closeErr := reader.Close(); closeErr != nil {
			formatErr = errors.Join(formatErr, &audioinput.Error{Kind: audioinput.KindClose, Path: path, Err: closeErr})
		}
		return nil, formatErr
	}
	return source, err
}
func resolveSessionAudioSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) (int, error) {
	inRate, outRate := plan.inputAudioSampleRate, plan.outputAudioSampleRate
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		config := requested.Request().Config
		service := audioinputwire.NewService(nil)
		inRate = service.PreferRate(inRate, int(config.InputAudioSampleRate))
		outRate = service.PreferRate(outRate, int(config.OutputAudioSampleRate))
	}
	fallback := audio.SampleRate
	if opts.ReplayPath == "" && (plan.provider == sessionProviderOpenAI || plan.provider == sessionProviderGrok) {
		fallback = sessionRealtimeAudioSampleRate
	}
	return audioinputwire.NewService(nil).NegotiateRate(inRate, outRate, fallback, ErrSessionAudioSampleRateConflict)
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
	return audioinputwire.NewService(nil).ConvertPCM(pcm, sourceRate, providerRate)
}
func convertScheduledAudioInputs(inputs []ScheduledAudioInput, providerRate int) ([]ScheduledAudioInput, error) {
	return audioinputwire.NewService(nil).ConvertScheduled(inputs, providerRate)
}
