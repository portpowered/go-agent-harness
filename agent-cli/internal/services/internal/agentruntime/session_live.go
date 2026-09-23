// This file contains live session-loop construction, operation, and lifecycle observation for the session command.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	sessiontransport "github.com/portpowered/go-agent-harness/agent-cli/internal/transport"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionturnwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/wire"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var errSessionMaxDurationExpired = errors.New("session max duration expired")

// ErrSessionScheduledAudioConfigTimeout identifies a live scheduled-audio run
// whose current session never acknowledged its initial configuration.
var ErrSessionScheduledAudioConfigTimeout = errors.New("scheduled audio session timed out awaiting session.updated")

// sessionFirstTurnAckTimeout bounds how long the SESSION.OPEN handler waits
// for the first user turn acceptance before failing the run instead of
// streaming user audio over an unacknowledged turn.
const sessionFirstTurnAckTimeout = 30 * time.Second

// sessionScheduledAudioConfigTimeout bounds the wait after SESSION.OPEN for a
// scheduled live session's initial SESSION.UPDATED acknowledgement. The
// per-loop override exists only for deterministic service tests.
const sessionScheduledAudioConfigTimeout = 30 * time.Second

func awaitSessionFirstTurnWithClock(ctx context.Context, ack <-chan error, audioService audioio.Service, source platformclock.Source) error {
	if audioService == nil {
		return errors.New("audio service is required for first-turn timing")
	}
	timer, err := audioService.NewTimer(source, sessionFirstTurnAckTimeout)
	if err != nil {
		return err
	}
	defer timer.Stop()
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return errors.New("timed out awaiting session first user turn acceptance")
	}
}

type sessionLoopOptions struct {
	Prompt         string
	PromptProvided bool
	CloseAfterOpen bool
	WaitForClose   bool
	MaxDuration    time.Duration
	// BareLive keeps a default-device voice session open until its owner
	// cancels it instead of applying the ordinary single-turn close policy.
	BareLive bool
	// BrowserToolsInteractive keeps a no-driver WebMCP live session open until
	// provider termination or explicit cancellation and selects its readiness
	// banner. It is intentionally distinct from BareLive so the session remains
	// truthfully identified as browser-enabled rather than bare.
	BrowserToolsInteractive bool
	// ListeningBanner is emitted after the provider's SESSION.CREATED event.
	// The enclosing plan fills it only after both local devices are open.
	ListeningBanner string
	// RequireAssistantResponse is enabled for finite audio-input sessions.
	// A tool-call MESSAGE.END is an intermediate provider turn; the session
	// must observe a later non-tool assistant MESSAGE.END before clean success.
	RequireAssistantResponse bool
	// RequireTerminalAssistantResponse applies the stricter terminal-output
	// contract to composed image-plus-audio turns. It is separate from
	// RequireAssistantResponse so existing audio-only callers may retain their
	// provider-close compatibility behavior while the multimodal turn rejects
	// a clean close with no assistant output.
	RequireTerminalAssistantResponse bool
	Done                             <-chan struct{}
	DoneErr                          func() error
	// AdmissionClosed marks the first phase of a room-bound shutdown. The
	// session may drain already-admitted provider output, but callers must not
	// enqueue another turn or tool continuation after this signal.
	AdmissionClosed <-chan struct{}
	// BoundCancellation marks the end of the room-bound grace window. It is
	// distinct from ordinary context cancellation so incomplete-response guards
	// can preserve clean room-owned cancellation semantics.
	BoundCancellation <-chan struct{}
	// AudioIn optionally streams a bounded file or stdin audio source into
	// the loop after SESSION.OPEN. When nil, every session path behaves
	// exactly as it did before audio input existed.
	AudioIn *sessionAudioSource

	// awaitFirstTurn optionally blocks the SESSION.OPEN handler until the
	// session's first user turn (the realtime image turn) has been accepted
	// by the provider session's outbound queue. Without it, streamed user
	// audio can overtake the still-propagating prompt turn and reorder the
	// customer's question after their speech on the wire. Nil preserves
	// existing behavior for every non-image session path.
	awaitFirstTurn <-chan error

	// observer optionally records per-turn and terminal diagnostics from the
	// consumed delta stream; nil keeps runtime behavior unchanged.
	observer *sessionProgressObserver

	// livenessClock is the participant-owned watchdog timer seam. Runtime plans
	// derive it from the public session clock when a caller does not inject one.
	livenessClock sessionduration.TimerScheduler
	// clockSource is the shared session timing domain. It is populated by the
	// runtime plan and is used for max-duration, acknowledgement, retry, and
	// configuration timers in the live stream path.
	clockSource  platformclock.Source
	audioService audioio.Service

	// toolLifecycleObserver records the exact call/result boundary owned by the
	// composed session executor. It is separate from the provider progress
	// observer because provider tool-call frames are requests, not executions.
	toolLifecycleObserver sessionToolLifecycleObserver

	// ToolExecutor is the composed session tool executor. When non-nil it is
	// wrapped once by newSessionToolExecutor and handed to
	// agentloop.WithToolExecutor so provider-originated realtime tool calls
	// execute through the product executor instead of the loop default.
	// Nil keeps loop construction byte-for-byte identical to today.
	ToolExecutor messages.ToolExecutor
	// toolDiagnostics receives original tool errors for the operator-facing
	// channel. The session adapter projects a separate customer-safe result.
	toolDiagnostics SessionToolDiagnosticSink

	// ToolDefinitions is the config-filtered tool surface advertised to the
	// session loop. It is paired with ToolExecutor by the runtime planner.
	ToolDefinitions []messages.ToolDefinition

	// InteractiveToolPolicy is the immutable per-session class and timeout
	// snapshot paired with ToolDefinitions and ToolExecutor.
	InteractiveToolPolicy runtimeTools.InteractiveToolPolicy
	// ToolDefinitionBase is the immutable static and stable broker surface
	// retained by the dynamic publisher while page definitions change.
	ToolDefinitionBase []messages.ToolDefinition
	// RefreshToolDefinitions returns the complete current tool surface after a
	// broker selection/catalog/generation event.
	RefreshToolDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	// BrowserWatch is an independent subscription to the broker's semantic
	// lifecycle observations. It is nil for sessions without browser tools.
	BrowserWatch func(context.Context) <-chan webmcp.BrokerEvent
	// PublicationTimerFactory controls dynamic catalog settle boundaries. Nil
	// selects the production wall-clock timer; tests may provide a deterministic
	// fake-clock implementation.
	PublicationTimerFactory webmcp.TimerFactory
	turnRuntime             sessionturn.Runtime
	turnBrowser             sessionturn.BrowserRequest

	// AdvertiseToolDefinitions sends the definitions through the generic
	// SESSION.UPDATE seam used by injected sessions. Live provider-backed
	// sessions receive definitions in their initial provider-specific config;
	// strict websocket replays preserve their captured outbound sequence.
	AdvertiseToolDefinitions bool

	// ToolExecutionTimeout overrides the per-invocation adapter deadline in
	// tests. Zero selects the class-specific interactive policy budget.
	ToolExecutionTimeout time.Duration

	// runtime stamps audio input and lifecycle observations from inside the
	// session command. Nil keeps the existing runtime path unchanged.
	runtime *sessionRuntimeObservationRecorder

	// cancellationIntent is the CLI-owned run marker used to distinguish an
	// operator SIGINT from ordinary caller cancellation.
	cancellationIntent SessionCancellationIntent

	// terminalSummaryRecorder receives a synthetic user-cancellation terminal
	// summary on the non-duration path. Duration artifacts already receive the
	// same summary through writeDurationSessionReplayMessage.
	terminalSummaryRecorder sessionduration.TerminalRecorder

	// terminalReporter is the services-owned consume-once boundary for the
	// customer-facing terminal announcement. Stream consumers only contribute
	// evidence; the enclosing runtime plan publishes it after finalization.
	terminalReporter *sessionTerminalReporter

	// AudioOutputError lets the audio-output wrapper report a concrete artifact
	// failure before the incomplete-response guard classifies a tool round trip.
	// Without this seam, malformed output can stop the wrapper before the final
	// assistant boundary and be misreported as an unresolved tool result.
	AudioOutputError func() error

	// rtcDeviceBinding is opened by the enclosing runtime plan and is started
	// against the real session-owned media endpoints after ConnectSession.
	rtcDeviceBinding runtimedevices.RTCBinding

	// CloseAfterScheduledAudio requests a live scheduled-audio session close
	// only after every queued input has produced a terminal assistant turn.
	// Replay plans leave this false so capture-derived close behavior remains
	// authoritative.
	CloseAfterScheduledAudio bool

	// ScheduledAudioDispatch is the explicit policy selected for repeated
	// scheduled audio. Runtime planning always supplies a non-zero value;
	// direct loop callers treat the zero value as completion-gated.
	ScheduledAudioDispatch ScheduledAudioDispatchPolicy
	// AudioInterruptions is the run-scoped channel for event-driven customer
	// audio. Inputs are sent through AgentLoop.SendAudioInput and their optional
	// MESSAGE.END boundary is sent through AgentLoop.SendSessionEvent, preserving
	// the normal provider ordering and barge-in behavior.
	AudioInterruptions <-chan ScheduledAudioInput
	// InputAudioSampleRate is the resolved provider-bound PCM16 rate used to
	// convert event-driven inputs before they enter AgentLoop.
	InputAudioSampleRate int

	// RequireSessionUpdated makes scheduled audio wait for the current
	// connection's initial SESSION.UPDATED acknowledgement before dispatch.
	// It is enabled for live OpenAI scheduled sessions; replay paths and other
	// session modes retain their existing lifecycle unless they opt in.
	RequireSessionUpdated bool
	// SessionUpdatedTimeout overrides the bounded readiness wait in tests. Zero
	// selects sessionScheduledAudioConfigTimeout.
	SessionUpdatedTimeout time.Duration

	// loopReady receives the constructed loop before its hot loop starts. The
	// self-play coordinator uses this to bind an io.Pipe reader to the peer's
	// session audio inbox without exposing the loop through SessionRunOptions.
	loopReady chan<- *agentloop.AgentLoop

	// quiesceUpstream stops an owner outside the session loop from producing new
	// outbound events while the shared terminal boundary performs its bounded
	// provider drain. Room replay uses this for its mixer; ordinary sessions
	// leave it nil.
	quiesceUpstream func() error
}

// duplexSessionLoopOptions is the single duplex loop construction seam. Both
// session runners build their loops here so an injected executor is configured once.
func duplexSessionLoopOptions(observedInferencer messages.SessionInferencer, opts sessionLoopOptions) []agentloop.Option {
	loopOpts := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	}
	toolExecutor := sessionLoopToolExecutor(opts)
	if toolExecutor != nil {
		if len(opts.ToolDefinitions) > 0 {
			loopOpts = append(loopOpts,
				agentloop.WithTools(opts.ToolDefinitions),
			)
			if opts.AdvertiseToolDefinitions {
				loopOpts = append(loopOpts, agentloop.WithSessionConfig(messages.SessionUpdateConfig{
					Tools: append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
				}))
			}
		}
		loopOpts = append(loopOpts, agentloop.WithToolExecutor(toolExecutor))
		if opts.InteractiveToolPolicy != nil {
			policy := opts.InteractiveToolPolicy.Clone()
			loopOpts = append(loopOpts, agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{
				Threshold: policy.Settings().AcknowledgementThreshold,
				IsLongRunning: func(name string) bool {
					return policy.ClassForTool(name) == runtimeTools.InteractiveToolClassBoundedLongRunning
				},
			}))
		}
	} else {
		loopOpts = append(loopOpts, agentloop.WithToolExecutionDisabled())
	}
	return loopOpts
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (runErr error) {
	return runSessionDurationInvocation(ctx, out, sessionInferencer, opts, opts.MaxDuration, opts.livenessClock, nil)
}

type realSessionDurationClock struct{}

func (realSessionDurationClock) NewTimer(interval time.Duration) sessionduration.Timer {
	return platformclock.Real{}.NewTimer(interval)
}

func runSessionDurationInvocation(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer) (runErr error) {
	reporter := opts.terminalReporter
	ownsReporter := reporter == nil
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		opts.terminalReporter = reporter
	}
	reporter.markRunStarted()
	renderer := terminalwire.NewService().NewTranscriptRenderer(out, reporter.observeStreamMessage)
	_, runErr = executeDurationRequest(ctx, renderer, inferencer, opts, maxDuration, clock, admitted)
	if ownsReporter {
		if err := renderer.Finish(); err != nil {
			runErr = errors.Join(runErr, err)
		}
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
	}
	return runErr
}

func writeDurationSessionReplayMessage(out io.Writer, msg messages.StreamMessage, artifacts sessionduration.ArtifactLifecycle) error {
	if artifacts != nil {
		if err := artifacts.Accept(msg); err != nil {
			return wrapSessionPhaseError("write duration artifacts", err)
		}
	}
	return terminalwire.NewService().WriteTranscriptMessage(out, msg)
}

func executeDurationRequest(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer) (sessionduration.Result, error) {
	durationService := durationwire.NewService()
	builder := newDurationRequestBuilder(ctx, out, inferencer, opts, maxDuration, clock, admitted, durationService)
	request := builder.Build()
	runner := sessionwire.NewDurationRunner(sessionwire.DurationDependencies{
		DurationService: durationService,
		LoopFactory:     sessionwire.NewDuplexLoopFactory(),
	})
	return runner.RunDuration(request)
}

type durationRequestBuilder struct {
	ctx         context.Context
	out         io.Writer
	inferencer  messages.SessionInferencer
	opts        sessionLoopOptions
	maxDuration time.Duration
	clock       sessionduration.TimerScheduler
	admitted    sessionduration.AdmissionInferencer
	service     sessionduration.Service
	artifacts   sessionduration.ArtifactLifecycle
	browser     sessionturn.BrowserRequest
}

func newDurationRequestBuilder(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock sessionduration.TimerScheduler, admitted sessionduration.AdmissionInferencer, service sessionduration.Service) *durationRequestBuilder {
	browser := opts.turnBrowser
	if browser.Watch == nil && opts.BrowserWatch != nil {
		browser = sessiontransport.SessionTurnBrowserRequest(opts.BrowserWatch, opts.RefreshToolDefinitions)
	}
	return &durationRequestBuilder{
		ctx: ctx, out: out, inferencer: inferencer, opts: opts, maxDuration: maxDuration,
		clock: clock, admitted: admitted, service: service,
		artifacts: service.ArtifactsFromContext(ctx), browser: browser,
	}
}

func (b *durationRequestBuilder) Build() runtimeSession.DurationRunRequest {
	request := runtimeSession.DurationRunRequest{
		Context:                    b.ctx,
		Inferencer:                 b.inferencer,
		Admission:                  b.admitted,
		Clock:                      b.clock,
		LivenessClock:              b.opts.livenessClock,
		MaxDuration:                b.maxDuration,
		Prompt:                     b.opts.Prompt,
		PromptProvided:             b.opts.PromptProvided,
		CloseAfterOpen:             b.opts.CloseAfterOpen,
		WaitForClose:               b.opts.WaitForClose,
		RequireAssistantResponse:   b.opts.RequireAssistantResponse,
		RequireTerminalReply:       b.opts.RequireTerminalAssistantResponse,
		CloseAfterScheduledAudio:   b.opts.CloseAfterScheduledAudio,
		ScheduledAudioDispatch:     sessionduration.ScheduledAudioDispatch(b.opts.ScheduledAudioDispatch),
		Observer:                   b.opts.observer,
		CompletionObserver:         b.opts.observer,
		SessionUpdatedTimeout:      b.opts.SessionUpdatedTimeout,
		SessionUpdatedTimeoutError: sessionScheduledAudioConfigTimeoutError(b.opts),
		Effects:                    b.effects(),
		Publication:                b.publication(),
		Done:                       b.opts.Done,
		DoneError:                  b.doneError(),
		Completion:                 b.completion(),
		ToolExecutor:               sessionLoopToolExecutor(b.opts),
		ToolDefinitions:            append([]messages.ToolDefinition(nil), b.opts.ToolDefinitions...),
		AdvertiseToolDefinitions:   b.opts.AdvertiseToolDefinitions,
		InteractiveToolPolicy:      b.opts.InteractiveToolPolicy,
		Binding:                    b.opts.rtcDeviceBinding,
		CloseProviderOnShutdown:    b.opts.BareLive,
		QuiesceUpstream:            b.opts.quiesceUpstream,
		StartPublication:           b.startPublication(),
		WrapInferencer:             b.wrapInferencer(),
		LoopReady:                  b.loopReady(),
		FinishObserver:             b.opts.observer.finishDuration,
		PublishUserCancellation:    b.publishUserCancellation(),
		AudioInput:                 b.audioInput(),
		AudioInterruptions:         b.audioInterruptions(),
	}
	return request
}

func (b *durationRequestBuilder) effects() sessionduration.RunEffects {
	return sessionduration.RunEffects{
		SessionCreated: func(_ context.Context, _ sessionduration.Loop) error {
			if b.opts.ListeningBanner != "" && b.out != nil {
				_, err := fmt.Fprintln(b.out, b.opts.ListeningBanner)
				return err
			}
			return nil
		},
		AwaitFirstTurn: func(waitCtx context.Context) error {
			if b.opts.awaitFirstTurn == nil {
				return nil
			}
			return awaitSessionFirstTurnWithClock(waitCtx, b.opts.awaitFirstTurn, b.opts.audioService, b.opts.clockSource)
		},
	}
}

func (b *durationRequestBuilder) publication() sessionduration.Publication {
	return sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
		return writeDurationSessionReplayMessage(b.out, msg, b.artifacts)
	}}
}

func (b *durationRequestBuilder) doneError() func() error {
	return func() error {
		if b.opts.DoneErr == nil {
			return nil
		}
		return b.opts.DoneErr()
	}
}

func (b *durationRequestBuilder) completion() runtimeSession.DurationCompletionOptions {
	return runtimeSession.DurationCompletionOptions{
		AudioOutputError:              b.opts.AudioOutputError,
		AssistantResponseIncomplete:   runtimeSession.ErrLiveAudioResponseIncomplete,
		ScheduledAudioIncompleteCause: runtimeSession.ErrLiveScheduledAudioIncomplete,
		BoundCancellation:             b.opts.BoundCancellation,
	}
}

func (b *durationRequestBuilder) startPublication() func(context.Context, sessionduration.Loop) (runtimeSession.DurationPublication, error) {
	return func(publishCtx context.Context, loop sessionduration.Loop) (runtimeSession.DurationPublication, error) {
		if b.browser.Watch == nil || b.browser.Refresh == nil {
			return nil, nil
		}
		sender, ok := loop.(sessionduration.SessionEventSender)
		if !ok {
			return nil, errors.New("session loop does not support tool publication")
		}
		publicationRequest := sessionturn.PublicationRequest{
			BaseDefinitions:    append([]messages.ToolDefinition(nil), b.opts.ToolDefinitionBase...),
			InitialDefinitions: append([]messages.ToolDefinition(nil), b.opts.ToolDefinitions...),
			Browser:            b.browser,
			Publish: func(ctx context.Context, definitions []messages.ToolDefinition) error {
				return sender.SendSessionEvent(ctx, messages.StreamMessage{
					Type:  messages.StreamTypeSessionUpdate,
					Value: messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: append([]messages.ToolDefinition(nil), definitions...)}),
				})
			},
		}
		if b.opts.turnRuntime != nil {
			return b.opts.turnRuntime.StartPublication(publishCtx, publicationRequest)
		}
		return sessionturnwire.NewDefaultService().StartPublication(publishCtx, publicationRequest)
	}
}

func (b *durationRequestBuilder) wrapInferencer() func(messages.SessionInferencer) (messages.SessionInferencer, runtimeSession.DurationSessionLifecycle, error) {
	return func(admitted messages.SessionInferencer) (messages.SessionInferencer, runtimeSession.DurationSessionLifecycle, error) {
		observed := newObservedSessionInferencer(admitted, b.opts.runtime)
		observed.progress = b.opts.observer
		return observed, observed, nil
	}
}

func (b *durationRequestBuilder) loopReady() func(sessionduration.Loop) error {
	return func(loop sessionduration.Loop) error {
		if b.opts.loopReady == nil {
			return nil
		}
		concrete, ok := loop.(*agentloop.AgentLoop)
		if !ok || concrete == nil {
			return errors.New("session loop adapter is invalid")
		}
		select {
		case b.opts.loopReady <- concrete:
			return nil
		case <-b.ctx.Done():
			return b.ctx.Err()
		}
	}
}

func (b *durationRequestBuilder) publishUserCancellation() func() error {
	return func() error {
		return publishSessionUserCancellation(b.out, b.opts, func(out io.Writer, msg messages.StreamMessage) error {
			return writeDurationSessionReplayMessage(out, msg, b.artifacts)
		})
	}
}

func (b *durationRequestBuilder) audioInput() sessionduration.AudioInputPort {
	if b.opts.AudioIn == nil {
		return sessionduration.AudioInputPort{}
	}
	return sessionduration.AudioInputPort{
		BindContext: b.opts.AudioIn.bindContext,
		Run: func(audioCtx context.Context, loop sessionduration.Loop) error {
			audioLoop, ok := loop.(sessionAudioLoop)
			if !ok || audioLoop == nil {
				return errors.New("session loop does not support audio input")
			}
			return streamSessionAudioInput(audioCtx, audioLoop, b.opts.AudioIn)
		},
	}
}

func (b *durationRequestBuilder) audioInterruptions() sessionduration.AudioInterruptionPort {
	return sessionduration.AudioInterruptionPort{
		Source: b.opts.AudioInterruptions,
		Dispatch: func(dispatchCtx context.Context, loop sessionduration.Loop, input ScheduledAudioInput) error {
			audioLoop, ok := loop.(sessionAudioLoop)
			if !ok || audioLoop == nil {
				return errors.New("session loop does not support audio interruption")
			}
			return sendEventDrivenAudioInput(dispatchCtx, audioLoop, b.opts, input)
		},
	}
}

func publishSessionUserCancellation(out io.Writer, opts sessionLoopOptions, write func(io.Writer, messages.StreamMessage) error) error {
	terminal := sessionUserCancelledTerminalMessage(opts.observer)
	var errs []error
	if opts.terminalSummaryRecorder != nil {
		summary, present, err := durationwire.NewService().RecordingTerminalSummaryFromMessage(terminal)
		if err != nil {
			errs = append(errs, fmt.Errorf("record user cancellation terminal summary: %w", err))
		} else if present {
			if err := opts.terminalSummaryRecorder.RecordTerminalSummary(*summary); err != nil {
				errs = append(errs, fmt.Errorf("record user cancellation terminal summary: %w", err))
			}
		}
	}
	if opts.terminalReporter != nil {
		if _, ok := out.(sessionterminal.TranscriptRenderer); ok && write != nil {
			if err := write(out, terminal); err != nil {
				errs = append(errs, err)
			}
		} else {
			opts.terminalReporter.observeStreamMessage(terminal, true)
		}
	} else if write != nil {
		if err := write(out, terminal); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func sessionUserCancelledTerminalMessage(observer *sessionProgressObserver) messages.StreamMessage {
	outputState := messages.TerminalOutputNone
	if observer != nil {
		outputState = observer.userCancellationOutputState()
	}
	return messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			SessionUserCancelledClassification,
			SessionUserCancelledClassification,
			messages.TerminalReasonCancellation,
			messages.TerminalProvenanceCLI,
			outputState,
		),
	}
}

// sessionRunTerminationError preserves a caller cancellation observed after
// the loop has already reported its terminal result. The session loop's
// select can receive both signals at once; cleanup intentionally filters the
// loop's expected context cancellation, but must not erase the caller's
// cancellation when the clean loop result wins that race.
func sessionRunTerminationError(ctx context.Context, err error) error {
	err = decorateSessionStreamTerminalError(err)
	if ctx == nil {
		return err
	}
	return errors.Join(err, ctx.Err())
}

type sessionStreamTerminalError struct {
	cause error
	text  string
}

func (e *sessionStreamTerminalError) Error() string { return e.text }
func (e *sessionStreamTerminalError) Unwrap() error { return e.cause }

// decorateSessionStreamTerminalError retains the human-readable message while
// exposing the structured provider classification at the CLI boundary. The
// agent loop returns StreamDeltaError after consuming a terminal provider
// event; without this decoration Cobra could only print the message text.
func decorateSessionStreamTerminalError(err error) error {
	if err == nil {
		return nil
	}
	var deltaErr *engine.StreamDeltaError
	if !errors.As(err, &deltaErr) || deltaErr.Value == nil {
		return err
	}
	fields := terminalwire.NewService().ErrorFields(deltaErr.Value)
	if fields == "" || strings.Contains(err.Error(), "classification=") {
		return err
	}
	message := strings.TrimSpace(deltaErr.Value.Message)
	if message == "" {
		message = "session error"
	}
	return &sessionStreamTerminalError{cause: err, text: fmt.Sprintf("%s [%s]", message, fields)}
}

func sessionScheduledAudioConfigTimeoutError(opts sessionLoopOptions) error {
	timeout := opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, timeout)
}

// shouldDispatchScheduledAudioForMessage identifies stream boundaries that can
// make the next scheduled input eligible. Completion-gated scheduling keeps
// its existing session/open, configuration, terminal, and tool-lifecycle
// wakeups. Active-response scheduling additionally wakes at the first live
// response boundary so the model runner can own the normal barge-in path.
func shouldDispatchScheduledAudioForMessage(msg messages.StreamMessage, policy ScheduledAudioDispatchPolicy) bool {
	switch msg.Type {
	case messages.StreamTypeSessionOpen, messages.StreamTypeMessageEnd, messages.StreamTypeSessionUpdated:
		return true
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		return policy == ScheduledAudioDispatchActiveResponse
	default:
		return false
	}
}
