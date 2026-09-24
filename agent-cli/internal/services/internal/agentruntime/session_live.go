// This file contains live session-loop construction, operation, and lifecycle observation for the session command.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

type sessionAudioLoop interface {
	SendAudioInput(context.Context, []byte) error
	SendSessionEvent(context.Context, messages.StreamMessage) error
}

func sendEventDrivenAudioInput(ctx context.Context, loop sessionAudioLoop, opts sessionLoopOptions, input sessiontrace.ScheduledAudioInput) error {
	if len(input.PCM) == 0 {
		return errors.New("event-driven audio input is empty")
	}
	if opts.audioService == nil {
		return errors.New("audio service is required for event-driven audio conversion")
	}
	pcm, err := opts.audioService.ConvertPCM16(ctx, audioio.PCM16Request{
		PCM: input.PCM, SourceRate: input.SourceSampleRate, TargetRate: opts.InputAudioSampleRate,
	})
	if err != nil {
		return fmt.Errorf("convert event-driven audio input: convert session input from %d Hz to provider rate %d Hz: %w", input.SourceSampleRate, opts.InputAudioSampleRate, err)
	}
	if err := loop.SendAudioInput(ctx, pcm); err != nil {
		return fmt.Errorf("send event-driven audio input: %w", err)
	}
	if opts.observer != nil {
		opts.observer.Account(metrics.DirectionInput, metrics.ModalityAudio, len(pcm))
	}
	if input.EndOfTurn {
		if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
			return fmt.Errorf("send event-driven audio input end-of-turn: %w", err)
		}
	}
	return nil
}

// ErrSessionScheduledAudioConfigTimeout identifies a live scheduled-audio run
// whose current session never acknowledged its initial configuration.
var ErrSessionScheduledAudioConfigTimeout = runtimeSession.ErrLiveSessionUpdatedTimeout

// sessionFirstTurnAckTimeout bounds how long the SESSION.OPEN handler waits
// for the first user turn acceptance before failing the run instead of
// streaming user audio over an unacknowledged turn.
const sessionFirstTurnAckTimeout = 30 * time.Second

// sessionScheduledAudioConfigTimeout bounds the wait after SESSION.OPEN for a
// scheduled live session's initial SESSION.UPDATED acknowledgement. The
// per-loop override exists only for deterministic service tests.
const sessionScheduledAudioConfigTimeout = 30 * time.Second

func awaitSessionFirstTurnWithClock(audioService audioio.Service, ctx context.Context, ack <-chan error, source platformclock.Source) error {
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
	audioService      audioio.Service
	durationService   sessionduration.Service
	durationRunner    runtimeSession.DurationRunner
	durationClock     sessionduration.TimerScheduler
	durationAdmission sessionduration.AdmissionInferencer
	Prompt            string
	PromptProvided    bool
	CloseAfterOpen    bool
	WaitForClose      bool
	MaxDuration       time.Duration
	closeTimeout      time.Duration
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
	// awaitFirstTurn optionally blocks the SESSION.OPEN handler until the
	// session's first user turn (the realtime image turn) has been accepted
	// by the provider session's outbound queue. Without it, streamed user
	// audio can overtake the still-propagating prompt turn and reorder the
	// customer's question after their speech on the wire. Nil preserves
	// existing behavior for every non-image session path.
	awaitFirstTurn <-chan error

	// observer optionally records per-turn and terminal diagnostics from the
	// consumed delta stream; nil keeps runtime behavior unchanged.
	observer sessiontrace.Observer

	// livenessClock is the participant-owned watchdog timer seam. Runtime plans
	// derive it from the public session clock when a caller does not inject one.
	livenessClock SessionLivenessClock
	// clockSource is the shared session timing domain. It is populated by the
	// runtime plan and is used for max-duration, acknowledgement, retry, and
	// configuration timers in the live stream path.
	clockSource platformclock.Source

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
	InteractiveToolPolicy *InteractiveToolPolicy
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
	runtime sessiontrace.RuntimeRecorder

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
	terminalReporter sessionterminal.Reporter

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

	// loopReady receives the constructed loop before its hot loop starts. A
	// room coordinator uses this to bind an io.Pipe reader to a peer's session
	// audio inbox without exposing the loop through SessionRunOptions.
	loopReady chan<- *agentloop.AgentLoop

	// quiesceUpstream stops an owner outside the session loop from producing new
	// outbound events while the shared terminal boundary performs its bounded
	// provider drain. Room replay uses this for its mixer; ordinary sessions
	// leave it nil.
	quiesceUpstream func() error
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (runErr error) {
	reporter, ownsReporter := sessionLoopReporter(opts)
	if opts.durationRunner == nil {
		return errors.New("session duration runner is required")
	}
	terminal := sessionterminalwire.NewService()
	renderer := terminal.NewTranscriptRenderer(out, reporter.ObserveStreamMessage)
	ports := sessionDurationRunPorts{
		ctx: ctx, out: out, opts: opts, terminal: terminal, renderer: renderer,
		artifacts: sessionDurationArtifacts(ctx, opts),
	}
	_, runErr = opts.durationRunner.RunDuration(ports.request(sessionInferencer))
	if ownsReporter {
		runErr = finishSessionLoopTranscript(renderer, reporter, out, runErr)
	}
	return runErr
}

type sessionDurationRunPorts struct {
	ctx       context.Context
	out       io.Writer
	opts      sessionLoopOptions
	terminal  sessionterminal.Service
	renderer  sessionterminal.TranscriptRenderer
	artifacts sessionduration.ArtifactLifecycle
}

func sessionLoopReporter(opts sessionLoopOptions) (sessionterminal.Reporter, bool) {
	reporter := opts.terminalReporter
	if reporter != nil {
		reporter.MarkRunStarted()
		return reporter, false
	}
	reporter = sessionterminalwire.NewReporter()
	reporter.MarkRunStarted()
	return reporter, true
}

func sessionDurationArtifacts(ctx context.Context, opts sessionLoopOptions) sessionduration.ArtifactLifecycle {
	if opts.durationService == nil {
		return nil
	}
	return opts.durationService.ArtifactsFromContext(ctx)
}

func finishSessionLoopTranscript(renderer sessionterminal.TranscriptRenderer, reporter sessionterminal.Reporter, out io.Writer, runErr error) error {
	if err := renderer.Finish(); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return errors.Join(runErr, reporter.Publish(out, runErr))
}

func (p sessionDurationRunPorts) request(inferencer messages.SessionInferencer) runtimeSession.DurationRunRequest {
	request := runtimeSession.DurationRunRequest{
		Context: p.ctx, Inferencer: inferencer, Admission: p.opts.durationAdmission,
		Clock: p.opts.durationClock, LivenessClock: durationLivenessClock(p.opts), MaxDuration: p.opts.MaxDuration,
		CloseTimeout: p.opts.closeTimeout, Prompt: p.opts.Prompt, PromptProvided: p.opts.PromptProvided,
		CloseAfterOpen: p.opts.CloseAfterOpen, WaitForClose: p.opts.WaitForClose,
		RequireAssistantResponse: p.opts.RequireAssistantResponse,
		RequireTerminalReply:     p.opts.RequireTerminalAssistantResponse,
		CloseAfterScheduledAudio: p.opts.CloseAfterScheduledAudio,
		ScheduledAudioDispatch:   sessionduration.ScheduledAudioDispatch(p.opts.ScheduledAudioDispatch),
		Observer:                 p.opts.observer, SessionUpdatedTimeout: p.opts.SessionUpdatedTimeout,
		SessionUpdatedTimeoutError: sessionScheduledAudioConfigTimeoutError(p.opts),
		Effects:                    p.effects(), Publication: p.publication(), Done: p.opts.Done, DoneError: p.opts.DoneErr,
		Completion: p.completion(), ToolExecutor: sessionLoopToolExecutor(p.opts),
		ToolDefinitions:          append([]messages.ToolDefinition(nil), p.opts.ToolDefinitions...),
		AdvertiseToolDefinitions: p.opts.AdvertiseToolDefinitions,
		Binding:                  p.opts.rtcDeviceBinding, CloseProviderOnShutdown: p.opts.BareLive,
		QuiesceUpstream:  p.opts.quiesceUpstream,
		StartPublication: p.startPublication, WrapInferencer: p.wrapInferencer,
		LoopReady: p.loopReady, FinishObserver: p.finishObserver,
		PublishUserCancellation: p.publishUserCancellation,
		AudioInterruptions:      sessionduration.AudioInterruptionPort{Source: p.opts.AudioInterruptions, Dispatch: p.dispatchAudioInterruption},
	}
	if p.opts.observer != nil {
		request.CompletionObserver = p.opts.observer
	}
	if p.opts.InteractiveToolPolicy != nil {
		request.InteractiveToolPolicy = p.opts.InteractiveToolPolicy.runtimePolicy
	}
	return request
}

func (p sessionDurationRunPorts) effects() sessionduration.RunEffects {
	return sessionduration.RunEffects{
		SessionCreated: p.sessionCreated, AwaitFirstTurn: p.awaitFirstTurn,
		DispatchScheduledInputs: p.dispatchScheduledInputs,
	}
}

func (p sessionDurationRunPorts) publication() sessionduration.Publication {
	return sessionduration.Publication{Write: p.writePublication}
}

func (p sessionDurationRunPorts) writePublication(msg messages.StreamMessage) error {
	if p.artifacts != nil {
		if err := p.artifacts.Accept(msg); err != nil {
			return fmt.Errorf("write duration artifacts: %w", err)
		}
	}
	return p.terminal.WriteTranscriptMessage(p.renderer, msg)
}

func (p sessionDurationRunPorts) completion() runtimeSession.DurationCompletionOptions {
	return runtimeSession.DurationCompletionOptions{
		AudioOutputError:              p.opts.AudioOutputError,
		AssistantResponseIncomplete:   runtimeSession.ErrLiveAudioResponseIncomplete,
		ScheduledAudioIncompleteCause: runtimeSession.ErrLiveScheduledAudioIncomplete,
		BoundCancellation:             p.opts.BoundCancellation,
	}
}

func (p sessionDurationRunPorts) sessionCreated(context.Context, sessionduration.Loop) error {
	if p.opts.ListeningBanner == "" || p.out == nil {
		return nil
	}
	_, err := fmt.Fprintln(p.out, p.opts.ListeningBanner)
	return err
}

func (p sessionDurationRunPorts) awaitFirstTurn(ctx context.Context) error {
	if p.opts.awaitFirstTurn == nil {
		return nil
	}
	return awaitSessionFirstTurnWithClock(p.opts.audioService, ctx, p.opts.awaitFirstTurn, p.opts.clockSource)
}

func (p sessionDurationRunPorts) dispatchScheduledInputs(ctx context.Context, loop sessionduration.Loop) error {
	if p.opts.observer == nil {
		return nil
	}
	sender, ok := loop.(sessiontrace.ScheduledInputSender)
	if !ok {
		return errors.New("session loop does not support scheduled audio input")
	}
	return p.opts.observer.DispatchScheduledInputs(ctx, sender)
}

func (p sessionDurationRunPorts) startPublication(ctx context.Context, loop sessionduration.Loop) (runtimeSession.DurationPublication, error) {
	concrete, ok := loop.(*agentloop.AgentLoop)
	if !ok || concrete == nil {
		return nil, errors.New("session loop adapter is invalid")
	}
	publisher, _ := startSessionDynamicToolPublisher(ctx, concrete, p.opts)
	return publisher, nil
}

func (p sessionDurationRunPorts) wrapInferencer(admitted messages.SessionInferencer) (messages.SessionInferencer, runtimeSession.DurationSessionLifecycle, error) {
	observed := newObservedSessionInferencer(admitted, p.opts.runtime)
	observed.progress = p.opts.observer
	return observed, observed, nil
}

func (p sessionDurationRunPorts) loopReady(loop sessionduration.Loop) error {
	if p.opts.loopReady == nil {
		return nil
	}
	concrete, ok := loop.(*agentloop.AgentLoop)
	if !ok || concrete == nil {
		return errors.New("session loop adapter is invalid")
	}
	select {
	case p.opts.loopReady <- concrete:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

func (p sessionDurationRunPorts) finishObserver(err error, _ bool) (error, bool) {
	if p.opts.observer == nil {
		return err, false
	}
	if roomChannelClosed(p.opts.BoundCancellation) {
		p.opts.observer.MarkRoomBoundCancellation()
	}
	userCancelled := sessionSIGINTCleanForObserver(err, p.opts.cancellationIntent, p.opts.observer)
	return p.opts.observer.Finish(err), userCancelled
}

func (p sessionDurationRunPorts) publishUserCancellation() error {
	terminalMessage := sessionUserCancelledTerminalMessage(p.opts.observer)
	if p.artifacts != nil {
		if err := p.artifacts.Accept(terminalMessage); err != nil {
			return fmt.Errorf("write duration artifacts: %w", err)
		}
	}
	if p.opts.terminalSummaryRecorder != nil {
		if summary, present, err := p.opts.durationService.RecordingTerminalSummaryFromMessage(terminalMessage); err != nil {
			return err
		} else if present {
			if err := p.opts.terminalSummaryRecorder.RecordTerminalSummary(*summary); err != nil {
				return err
			}
		}
	}
	return p.terminal.WriteTranscriptMessage(p.renderer, terminalMessage)
}

func (p sessionDurationRunPorts) dispatchAudioInterruption(ctx context.Context, loop sessionduration.Loop, input sessiontrace.ScheduledAudioInput) error {
	audioLoop, ok := loop.(sessionAudioLoop)
	if !ok {
		return errors.New("session loop does not support audio interruptions")
	}
	return sendEventDrivenAudioInput(ctx, audioLoop, p.opts, input)
}

func durationLivenessClock(opts sessionLoopOptions) sessionduration.TimerScheduler {
	if opts.durationClock != nil {
		return opts.durationClock
	}
	if source, ok := opts.clockSource.(platformclock.TimerSource); ok {
		return source
	}
	return platformclock.Real{}
}

func sessionLoopToolExecutor(opts sessionLoopOptions) messages.ToolExecutor {
	if opts.ToolExecutor == nil {
		return nil
	}
	return newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(
		opts.ToolExecutor,
		opts.InteractiveToolPolicy,
		opts.ToolExecutionTimeout,
		composeSessionToolLifecycleObserver(opts.toolLifecycleObserver, opts.observer, opts.runtime),
		opts.cancellationIntent,
		opts.toolDiagnostics,
	)
}

func sessionUserCancelledTerminalMessage(observer sessiontrace.Observer) messages.StreamMessage {
	outputState := messages.TerminalOutputNone
	if observer != nil {
		outputState = observer.UserCancellationOutputState()
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

func sessionSIGINTCleanForObserver(err error, intent SessionCancellationIntent, observer sessiontrace.Observer) bool {
	if intent == nil || !intent.SIGINTReceived() || observer == nil {
		return false
	}
	return observer.CancellationClean(err)
}

func sessionScheduledAudioConfigTimeoutError(opts sessionLoopOptions) error {
	timeout := opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, timeout)
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}

func (s *observedSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *observedSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}
