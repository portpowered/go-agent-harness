// This file contains live session-loop construction, operation, and lifecycle observation for the session command.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var errSessionMaxDurationExpired = sessionterminal.ErrDurationExpired

// ErrSessionScheduledAudioIncomplete identifies a live scheduled-audio run
// that ended before every queued input received an assistant response.
var ErrSessionScheduledAudioIncomplete = errors.New("scheduled audio session ended before all turns completed")

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func sendEventDrivenAudioInput(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, input sessiontrace.ScheduledAudioInput) error {
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

// SessionScheduledAudioIncompleteError carries the deterministic schedule
// counts observed at a terminal boundary. When a provider terminal caused the
// incomplete lifecycle, its bounded status, error code, and detail are retained
// as well. It unwraps to ErrSessionScheduledAudioIncomplete so callers can use
// errors.Is while still retaining any provider, timeout, cancellation, or
// cleanup cause joined with it.
type SessionScheduledAudioIncompleteError struct {
	Completed         int
	Dispatched        int
	Scheduled         int
	ProviderStatus    string
	ProviderErrorCode string
	ProviderDetails   string
}

func (e *SessionScheduledAudioIncompleteError) Error() string {
	if e == nil {
		return ErrSessionScheduledAudioIncomplete.Error()
	}
	message := fmt.Sprintf("%s: completed=%d dispatched=%d scheduled=%d", ErrSessionScheduledAudioIncomplete, e.Completed, e.Dispatched, e.Scheduled)
	annotations := make([]string, 0, 3)
	if status := strings.TrimSpace(e.ProviderStatus); status != "" {
		annotations = append(annotations, "status="+status)
	}
	if code := strings.TrimSpace(e.ProviderErrorCode); code != "" {
		annotations = append(annotations, "code="+code)
	}
	if detail := strings.TrimSpace(e.ProviderDetails); detail != "" {
		annotations = append(annotations, "detail="+detail)
	}
	if len(annotations) > 0 {
		message += " (" + strings.Join(annotations, "; ") + ")"
	}
	return message
}

func (e *SessionScheduledAudioIncompleteError) Unwrap() error {
	return ErrSessionScheduledAudioIncomplete
}

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
	audioService   audioio.Service
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
	terminalSummaryRecorder sessionDurationTerminalRecorder

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

// duplexSessionLoopOptions is the single duplex loop construction seam. Both
// the plain and duration-bounded session runners build their loops here so an
// injected executor enables tool execution exactly once per session.
func duplexSessionLoopOptions(observedInferencer messages.SessionInferencer, opts sessionLoopOptions) []agentloop.Option {
	loopOpts := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	}
	if opts.ToolExecutor != nil {
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
		loopOpts = append(loopOpts, agentloop.WithToolExecutor(newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(
			opts.ToolExecutor,
			opts.InteractiveToolPolicy,
			opts.ToolExecutionTimeout,
			composeSessionToolLifecycleObserver(opts.toolLifecycleObserver, opts.observer, opts.runtime),
			opts.cancellationIntent,
			opts.toolDiagnostics,
		)))
		if opts.InteractiveToolPolicy != nil {
			policy := opts.InteractiveToolPolicy.Clone()
			loopOpts = append(loopOpts, agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{
				Threshold: policy.AcknowledgementThreshold,
				IsLongRunning: func(name string) bool {
					return policy.ClassForTool(name) == InteractiveToolClassBoundedLongRunning
				},
			}))
		}
	} else {
		loopOpts = append(loopOpts, agentloop.WithToolExecutionDisabled())
	}
	return loopOpts
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (runErr error) {
	reporter := opts.terminalReporter
	ownsReporter := reporter == nil
	if reporter == nil {
		reporter = sessionterminalwire.NewReporter()
		opts.terminalReporter = reporter
	}
	reporter.MarkRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	runErr = runAgentLoopSessionStream(ctx, renderer, sessionInferencer, opts)
	if !roomChannelClosed(opts.BoundCancellation) {
		runErr = audioResponseCompletionError(runErr, opts)
		runErr = scheduledAudioCompletionError(runErr, opts)
	} else if opts.observer != nil {
		// A bound cancellation is an intentional room-owned terminal path. Mark
		// it before finish so unresolved tool work and incomplete-response guards
		// cannot turn the deliberate teardown into a session failure.
		opts.observer.MarkRoomBoundCancellation()
	}
	cleanSIGINT := sessionSIGINTCleanForObserver(runErr, opts.cancellationIntent, opts.observer)
	if opts.observer != nil {
		runErr = opts.observer.Finish(runErr)
	}
	if cleanSIGINT {
		runErr = errors.Join(runErr, publishSessionUserCancellation(renderer, opts, writeSessionReplayMessage))
	}
	if ownsReporter {
		if err := renderer.finishTranscript(); err != nil {
			runErr = errors.Join(runErr, err)
		}
		runErr = errors.Join(runErr, reporter.Publish(out, runErr))
	}
	return runErr
}

func publishSessionUserCancellation(out io.Writer, opts sessionLoopOptions, write func(io.Writer, messages.StreamMessage) error) error {
	terminal := sessionUserCancelledTerminalMessage(opts.observer)
	var errs []error
	if opts.terminalSummaryRecorder != nil {
		summary, present, err := recordingTerminalSummaryFromMessage(terminal)
		if err != nil {
			errs = append(errs, fmt.Errorf("record user cancellation terminal summary: %w", err))
		} else if present {
			if err := opts.terminalSummaryRecorder.RecordTerminalSummary(*summary); err != nil {
				errs = append(errs, fmt.Errorf("record user cancellation terminal summary: %w", err))
			}
		}
	}
	if opts.terminalReporter != nil {
		if _, ok := out.(sessionReplayMessageWriter); ok && write != nil {
			if err := write(out, terminal); err != nil {
				errs = append(errs, err)
			}
		} else {
			opts.terminalReporter.ObserveStreamMessage(terminal, true)
		}
	} else if write != nil {
		if err := write(out, terminal); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

// audioResponseCompletionError prevents an audio-input session from reporting
// clean success without a terminal assistant response. It also preserves the
// older tool-continuation guard for callers that do not set RequireAssistantResponse.
func audioResponseCompletionError(err error, opts sessionLoopOptions) error {
	if opts.AudioOutputError != nil {
		// Await output before applying incomplete-response precedence.
		if outputErr := opts.AudioOutputError(); outputErr != nil {
			err = errors.Join(err, outputErr)
		}
	}
	if opts.RequireTerminalAssistantResponse && (opts.observer == nil || !opts.observer.AssistantResponseCompleted()) {
		incomplete := ErrSessionAudioResponseIncomplete
		if err == nil {
			return incomplete
		}
		return errors.Join(err, incomplete)
	}
	if opts.observer == nil || !opts.observer.ProviderToolCallObserved() || opts.observer.AssistantResponseCompleted() {
		return err
	}
	incomplete := ErrSessionAudioResponseIncomplete
	if err == nil {
		return incomplete
	}
	return errors.Join(err, incomplete)
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
	fields := sessionErrorFields(deltaErr.Value)
	if fields == "" || strings.Contains(err.Error(), "classification=") {
		return err
	}
	message := strings.TrimSpace(deltaErr.Value.Message)
	if message == "" {
		message = "session error"
	}
	return &sessionStreamTerminalError{cause: err, text: fmt.Sprintf("%s [%s]", message, fields)}
}

func scheduledAudioCompletionError(err error, opts sessionLoopOptions) error {
	if !opts.CloseAfterScheduledAudio || opts.observer == nil || !opts.observer.ScheduledAudioIncomplete() {
		return err
	}
	if errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		return err
	}
	completed, dispatched, scheduled := opts.observer.ScheduledAudioCounts()
	providerStatus, providerCode, providerDetails := opts.observer.ScheduledAudioFailureMetadata()
	incomplete := &SessionScheduledAudioIncompleteError{
		Completed:         completed,
		Dispatched:        dispatched,
		Scheduled:         scheduled,
		ProviderStatus:    providerStatus,
		ProviderErrorCode: providerCode,
		ProviderDetails:   providerDetails,
	}
	if err == nil {
		return incomplete
	}
	return errors.Join(err, incomplete)
}

func sessionSIGINTCleanForObserver(err error, intent SessionCancellationIntent, observer sessiontrace.Observer) bool {
	if intent == nil || !intent.SIGINTReceived() || observer == nil {
		return false
	}
	return observer.CancellationClean(err)
}

func sessionErrorIsCancellation(err error) bool {
	if err == nil || sessionterminalwire.HasIndependentFailure(err) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func sessionScheduledAudioConfigTimeoutError(opts sessionLoopOptions) error {
	timeout := opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, timeout)
}

type sessionLoopMessageState struct {
	promptSent            bool
	closeSent             bool
	closeAfterOpenPending bool
	listeningReported     bool
}

type playbackDrainingSession interface {
	DrainPlayback(context.Context) error
}

func closeBareSessionIfNeeded(bare bool, inferencer *observedSessionInferencer) error {
	if !bare || inferencer == nil {
		return nil
	}
	return inferencer.CloseSession()
}

func handleSessionLoopMessage(ctx context.Context, sessionDone <-chan struct{}, deadline <-chan time.Time, out io.Writer, loop *agentloop.AgentLoop, opts sessionLoopOptions, msg messages.StreamMessage, state sessionLoopMessageState, terminate func(error) error) (sessionLoopMessageState, bool, error) {
	if opts.observer != nil {
		opts.observer.Observe(msg)
	}
	if err := writeSessionReplayMessage(out, msg); err != nil {
		return state, false, terminate(err)
	}
	if opts.observer != nil {
		if livenessErr := opts.observer.LivenessFailure(); livenessErr != nil {
			return state, false, terminate(livenessErr)
		}
	}
	if msg.Type == messages.StreamTypeSessionCreated && (opts.BareLive || opts.BrowserToolsInteractive) && opts.ListeningBanner != "" && !state.listeningReported {
		if _, err := fmt.Fprintln(out, opts.ListeningBanner); err != nil {
			return state, false, terminate(err)
		}
		state.listeningReported = true
	}
	if err := retryScheduledRateLimitedResponseWithClock(ctx, sessionDone, deadline, loop, opts.observer, msg, opts.clockSource); err != nil {
		if errors.Is(err, errSessionMaxDurationExpired) {
			// The caller owns the clean terminal transition for a stop result.
			// Returning the internal deadline sentinel here would both expose an
			// implementation detail and make the caller skip its single shared
			// termination boundary.
			return state, true, nil
		}
		return state, false, terminate(err)
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		var openErr error
		state, openErr = handleLiveSessionOpen(ctx, loop, opts, state)
		if openErr != nil {
			return state, false, terminate(openErr)
		}
	}
	if shouldDispatchScheduledAudioForMessage(msg, opts.ScheduledAudioDispatch) {
		if opts.observer != nil {
			if err := opts.observer.DispatchScheduledInputs(ctx, loop); err != nil {
				return state, false, terminate(err)
			}
		}
	}
	if opts.CloseAfterOpen && (opts.PromptProvided || opts.Prompt != "") && msg.Type == messages.StreamTypeMessageEnd && !state.closeSent && (opts.observer == nil || opts.observer.LastMessageEndAdmitted()) {
		state.closeAfterOpenPending = true
		var closeErr error
		state, closeErr = closePendingSessionIfReady(ctx, loop, opts, state)
		if closeErr != nil {
			return state, false, terminate(closeErr)
		}
	}
	if opts.CloseAfterScheduledAudio && msg.Type == messages.StreamTypeMessageEnd && (opts.observer == nil || opts.observer.LastMessageEndAdmitted()) {
		var closeErr error
		state, closeErr = closePendingSessionIfReady(ctx, loop, opts, state)
		if closeErr != nil {
			return state, false, terminate(closeErr)
		}
	}
	if shouldStopSessionLoop(msg, opts) {
		return state, true, nil
	}
	return state, false, nil
}

func retryScheduledRateLimitedResponseWithClock(ctx context.Context, sessionDone <-chan struct{}, deadline <-chan time.Time, loop *agentloop.AgentLoop, observer sessiontrace.Observer, msg messages.StreamMessage, source platformclock.Source) error {
	if observer == nil || loop == nil || msg.Type != messages.StreamTypeMessageEnd {
		return nil
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || terminal == nil {
		return nil
	}
	if sessionDurationTimerReady(deadline) {
		return errSessionMaxDurationExpired
	}
	delay, retry := observer.ClaimScheduledRateLimitRetry(msg.ResponseID, terminal)
	if !retry {
		return nil
	}
	timerSource, err := platformclock.RequireTimerSource(source)
	if err != nil {
		return err
	}
	timer := timerSource.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C():
		if sessionDurationTimerReady(deadline) {
			return errSessionMaxDurationExpired
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-sessionDone:
			return context.Canceled
		default:
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-sessionDone:
		return context.Canceled
	case <-deadline:
		return errSessionMaxDurationExpired
	}
	if sessionDurationTimerReady(deadline) {
		return errSessionMaxDurationExpired
	}
	select {
	case <-sessionDone:
		return context.Canceled
	default:
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}); err != nil {
		return fmt.Errorf("send rate-limit retry response: %w", err)
	}
	observer.ObserveProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	return nil
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

// newSessionLiveTerminationBoundary builds the shared termination boundary
// for the live session loop. It is factored out of runAgentLoopSessionStream
// so that function's select loop — which owns a dozen distinct terminal exit
// paths — stays under the enforced length limit; the boundary itself is what
// keeps every one of those paths consistent through the public terminal
// service boundary.
func newSessionLiveTerminationBoundary(
	ctx context.Context,
	quiesceUpstream func() error,
	stopOwnedResources func() error,
	out io.Writer,
	loop *agentloop.AgentLoop,
	opts sessionLoopOptions,
	observedInferencer *observedSessionInferencer,
) sessionterminal.TerminationBoundary {
	return sessionterminalwire.NewTerminationBoundary(sessionterminal.TerminationOptions{
		Context:         ctx,
		QuiesceUpstream: quiesceUpstream,
		WaitForStragglers: func() error {
			// Keep the injected clock as the canonical quiet-period source so
			// runtime timestamps and scheduling remain in one domain. The drain
			// itself also has a wall-time safety bound for deterministic clocks:
			// teardown can begin after the last virtual tick, and cleanup must not
			// wait forever for a timer that no owner can advance anymore.
			source := opts.clockSource
			return waitForSessionLoopStragglersWithContext(ctx, out, loop, sessionStragglerDrainPolicy{quietPeriod: sessionterminal.StragglerDrainQuietPeriod}, opts.observer, source, opts.audioService)
		},
		StopOwnedResources: stopOwnedResources,
		FlushBuffered: func() error {
			flushErr := flushBufferedSessionLoopMessages(out, loop, opts.observer)
			if opts.observer != nil {
				// The engine may have committed a provider tool delta to conversation
				// history before cancellation prevented the consumer-facing outbox from
				// delivering it. Recover only provider tool lifecycle identity after the
				// hot loop is stopped, avoiding duplicate output accounting.
				opts.observer.ObserveBufferedProviderToolLifecycle(loop.GetConversationDeltas())
			}
			if sessionErr := observedInferencer.sessionFailure(); sessionErr != nil {
				flushErr = errors.Join(flushErr, fmt.Errorf("session transport: %w", sessionErr))
			}
			return flushErr
		},
	})
}

func runAgentLoopSessionStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (runErr error) {
	var finishOutput func(error) error
	out, finishOutput = prepareSessionStreamOutput(out, &opts)
	defer func() { runErr = finishOutput(runErr) }()
	if opts.observer != nil {
		defer opts.observer.StopLiveness()
	}
	loop, observedInferencer, rtcPumpErrors, err := newObservedSessionLoop(sessionInferencer, opts)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Audio input is an upstream producer owned by this session, but it must
	// stop before the shared terminal boundary begins waiting for provider
	// stragglers. Keep its cancellation scope separate from runCtx so the
	// provider output drain can still run after the producer is quiesced.
	publisher, publisherErrors := startSessionDynamicToolPublisher(runCtx, loop, opts)
	var livenessErrors <-chan error
	if opts.observer != nil {
		livenessErrors = opts.observer.LivenessErrors(runCtx)
	}
	publisherErrors = sessiontracewire.MergeErrorChannels(runCtx, publisherErrors, livenessErrors)
	defer publisher.stop()
	if err := bindSessionLoopInputs(runCtx, loop, opts); err != nil {
		return err
	}

	timeout, stopTimeout, err := sessionStreamDeadline(opts)
	if err != nil {
		return err
	}
	defer stopTimeout()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()

	runDone := false
	runErrSelectCh := (<-chan error)(runErrCh)
	waitRun := func() error {
		if !runDone {
			select {
			case runErr = <-runErrCh:
				runDone = true
			case <-ctx.Done():
				// Bound provider close waits by the owning context.
				return ctx.Err()
			}
		}
		return runErr
	}
	quiesceUpstream := opts.quiesceUpstream
	drainDevicePlayback := false
	stopOwnedResources := func() error {
		var drainErr error
		if drainDevicePlayback {
			drainErr = observedInferencer.DrainSessionPlayback(ctx)
		}
		cancel()
		providerErr := closeBareSessionIfNeeded(opts.BareLive, observedInferencer)
		var runTerminationErr error
		if runErr := waitRun(); runErr != nil && !sessionErrorIsCancellation(runErr) {
			runTerminationErr = fmt.Errorf("session error: %w", runErr)
		}
		return errors.Join(drainErr, providerErr, runTerminationErr)
	}
	termination := newSessionLiveTerminationBoundary(ctx, quiesceUpstream, stopOwnedResources, out, loop, opts, observedInferencer)
	terminate := termination.Terminate
	terminateWithPlaybackDrain := func(err error) error {
		drainDevicePlayback = err == nil && ctx.Err() == nil
		return terminate(err)
	}

	var sessionUpdatedTimer platformclock.Timer
	var sessionUpdatedTimeout <-chan time.Time
	var sessionUpdatedTimerErr error
	defer func() { stopLiveSessionUpdatedTimer(&sessionUpdatedTimer, &sessionUpdatedTimeout) }()

	state := sessionLoopMessageState{}
	// awaitingResponse keeps finite audio EOF from ending an admitted turn early.
	awaitingResponse := true
	done := opts.Done
	providerDone := observedInferencer.Done()
	admissionClosed := opts.AdmissionClosed
	boundCancellation := opts.BoundCancellation
	audioInterruptions := opts.AudioInterruptions
	var toolLifecycleEvents <-chan struct{}
	if opts.observer != nil {
		toolLifecycleEvents = opts.observer.ToolLifecycleEvents()
	}
	handleDelta := func(msg messages.StreamMessage) (bool, error) {
		nextState, stopLoop, msgErr := handleSessionLoopMessage(runCtx, providerDone, timeout, out, loop, opts, msg, state, terminate)
		state = nextState
		if msgErr != nil {
			return false, msgErr
		}
		if msg.Type == messages.StreamTypeSessionCreated {
			// SESSION.UPDATE is sent while handling SESSION.CREATED. Release
			// dynamic publication only after that bootstrap boundary is observed.
			publisher.markSessionReady()
		}
		if msg.Type == messages.StreamTypeSessionOpen {
			sessionUpdatedTimer, sessionUpdatedTimeout, sessionUpdatedTimerErr = startLiveSessionUpdatedTimer(opts, sessionUpdatedTimer)
		}
		if sessionUpdatedTimerErr != nil {
			return false, terminate(sessionUpdatedTimerErr)
		}
		if opts.observer != nil && opts.observer.ScheduledAudioReady() {
			stopLiveSessionUpdatedTimer(&sessionUpdatedTimer, &sessionUpdatedTimeout)
		}
		return stopLoop, nil
	}
	drainPublishedDeltas := func() (bool, error) {
		return drainPublishedSessionDeltas(loop.Deltas().Read, handleDelta)
	}
	for {
		select {
		case <-admissionClosed:
			admissionClosed = nil
			audioInterruptions = nil
			toolLifecycleEvents = nil
		case <-boundCancellation:
			boundCancellation = nil
			return terminate(nil)
		case publicationErr := <-publisherErrors:
			return terminate(publicationErr)
		case input, ok := <-audioInterruptions:
			if !ok {
				audioInterruptions = nil
				continue
			}
			if err := sendEventDrivenAudioInput(runCtx, loop, opts, input); err != nil {
				return terminate(err)
			}
		case <-toolLifecycleEvents:
			// A tool lifecycle transition can make the next scheduled audio
			// input eligible without producing a provider delta. Re-run the
			// scheduler on the same serialized session-loop goroutine before
			// evaluating close, so result acceptance and continuation
			// completion cannot strand the next turn.
			if opts.observer != nil {
				if err := opts.observer.DispatchScheduledInputs(runCtx, loop); err != nil {
					return terminate(err)
				}
			}
			var closeErr error
			state, closeErr = closePendingSessionIfReady(runCtx, loop, opts, state)
			if closeErr != nil {
				return terminate(closeErr)
			}
		case pumpErr := <-rtcPumpErrors:
			return terminate(pumpErr)
		case <-done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			return terminate(doneErr)
		case <-timeout:
			return terminate(nil)
		case <-sessionUpdatedTimeout:
			stopLiveSessionUpdatedTimer(&sessionUpdatedTimer, &sessionUpdatedTimeout)
			return terminate(sessionScheduledAudioConfigTimeoutError(opts))
		case <-ctx.Done():
			if awaitingResponse {
				return terminate(fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", ctx.Err()))
			}
			return sessionRunTerminationError(ctx, terminate(nil))
		case <-providerDone:
			if connectErr := observedInferencer.connectFailure(); connectErr != nil {
				return terminate(fmt.Errorf("session connect: %w", connectErr))
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				if awaitingResponse {
					return terminate(fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", ctxErr))
				}
				return sessionRunTerminationError(ctx, terminate(nil))
			}
			// The session runner emits a synthetic provider-close delta after it
			// drains any messages already queued by the provider. AgentLoop.Run
			// may report its hot-loop completion before the delta-forwarding
			// goroutine publishes that final message. Keep the provider signal
			// disabled after observing it and let the terminal delta reach this
			// loop, so a final audio frame cannot be discarded by teardown.
			providerDone = nil
			continue
		case err := <-runErrSelectCh:
			runErr = err
			runDone = true
			runErrSelectCh = nil
			if runErr == nil {
				// AgentLoop.Run joins its forwarding worker before returning, so
				// every kernel delta is already in the public buffer. Consume it
				// before teardown. Waiting for providerDone is unsafe because a
				// clean loop completion cancels the model runner while its provider
				// transport may intentionally keep the socket open.
				stopLoop, drainErr := drainPublishedDeltas()
				if drainErr != nil {
					return drainErr
				}
				if stopLoop {
					return terminateWithPlaybackDrain(nil)
				}
			}
			if ctxErr := ctx.Err(); ctxErr != nil && awaitingResponse {
				return terminate(fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", ctxErr))
			}
			if runErr == nil && ctx.Err() == nil {
				return sessionRunTerminationError(ctx, terminateWithPlaybackDrain(nil))
			}
			return sessionRunTerminationError(ctx, terminate(nil))
		case msg := <-loop.Deltas().Chan():
			stopLoop, msgErr := handleDelta(msg)
			if msgErr != nil {
				return msgErr
			}
			if stopLoop {
				return terminateWithPlaybackDrain(nil)
			}
		}
	}
}

// closePendingSessionIfReady is shared by response handling and the
// asynchronous tool-result acceptance wake-up. A final accepted result may
// arrive after the final response.done, so closure must be re-evaluated from
// both paths.
func closePendingSessionIfReady(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, state sessionLoopMessageState) (sessionLoopMessageState, error) {
	if state.closeSent {
		return state, nil
	}
	if opts.observer != nil && opts.observer.HasToolLifecycleObligation() {
		return state, nil
	}
	closeAfterOpen := opts.CloseAfterOpen && state.closeAfterOpenPending
	closeAfterScheduled := opts.CloseAfterScheduledAudio && opts.observer != nil && opts.observer.ScheduledAudioComplete()
	if !closeAfterOpen && !closeAfterScheduled {
		return state, nil
	}
	if err := sendSessionClose(ctx, loop); err != nil {
		return state, err
	}
	state.closeSent = true
	return state, nil
}

func newDurationStragglerTimer(clock SessionDurationClock, quiet time.Duration) (SessionDurationTimer, error) {
	if clock == nil {
		return nil, errors.New("session duration clock is required for straggler drain")
	}
	timer := clock.NewTimer(quiet)
	if timer == nil {
		return nil, errors.New("session duration clock returned a nil straggler timer")
	}
	return timer, nil
}

func resetDurationStragglerTimer(timer SessionDurationTimer, clock SessionDurationClock, quiet time.Duration) (SessionDurationTimer, error) {
	stopAndDrainSessionTimer(timer)
	return newDurationStragglerTimer(clock, quiet)
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}

// drainPublishedSessionDeltas consumes the finite set of messages already
// published to the session loop's public delta buffer. AgentLoop.Run's clean
// completion is a publication barrier, so callers must inspect this buffer
// before treating the run result as the terminal boundary.
func drainPublishedSessionDeltas(read func() (messages.StreamMessage, bool), handle func(messages.StreamMessage) (bool, error)) (bool, error) {
	if read == nil || handle == nil {
		return false, nil
	}
	for {
		msg, ok := read()
		if !ok {
			return false, nil
		}
		stopLoop, err := handle(msg)
		if err != nil || stopLoop {
			return stopLoop, err
		}
	}
}
