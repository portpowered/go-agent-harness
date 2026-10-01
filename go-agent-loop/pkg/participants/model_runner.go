package participants

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants/internal/sessionstate"
)

// ModelRunner is the model participant. In turn-based mode it reads
// InferenceRequests from Inbox and streams each response to DeltaOutbox. In
// session mode it owns a persistent provider session and its own event loop:
// provider events flow to DeltaOutbox, user input flows in through
// EnqueueSessionInput, and Inbox carries the coordinator's result-driven
// requests (tool results and user text).
type ModelRunner struct {
	inferencer        messages.Inferencer
	sessionInferencer messages.SessionInferencer
	sessionConfig     *messages.SessionUpdateConfig // sent as SESSION.UPDATE on the first SESSION.OPEN or SESSION.CREATED
	Inbox             *messages.TypedBuffer[messages.InferenceRequest]
	DeltaOutbox       *messages.TypedBuffer[messages.StreamMessage]

	// ingress is the ordered user-input queue; nil outside session mode.
	ingress *sessionIngress

	streamID      string // set at start of each inference (one stream per request)
	actorIndex    int    // incremented for each delta written to DeltaOutbox
	currentPassID int    // LoopPassID from the current InferenceRequest

	// sessionToolContinuation records the result of the session loop's
	// explicit tool-result boundary. It lets the request-driven compatibility
	// helper distinguish a continuation already queued by ToolResultForwarder
	// from an isolated caller that still needs to request one. It is owned by
	// the session goroutine.
	sessionToolContinuation sessionToolContinuationState

	bargeInConfig *BargeInConfig    // nil selects DefaultBargeInConfig
	clock         clock.TimerSource // times held onset audio; nil selects the real clock

	execMu     sync.Mutex
	execCancel context.CancelFunc // cancel for the current per-execution context; nil when idle
}

type sessionToolContinuationState uint8

const (
	sessionToolContinuationNone sessionToolContinuationState = iota
	sessionToolContinuationAccepted
	sessionToolContinuationSuppressed
)

func NewModelRunner(inferencer messages.Inferencer, bufferCapacity int) *ModelRunner {
	return &ModelRunner{
		inferencer:  inferencer,
		Inbox:       messages.NewTypedBuffer[messages.InferenceRequest](bufferCapacity),
		DeltaOutbox: messages.NewTypedBuffer[messages.StreamMessage](bufferCapacity),
	}
}

// NewSessionModelRunner creates a ModelRunner in duplex session mode.
// Instead of processing InferenceRequest from Inbox, it establishes a
// persistent session via the given SessionInferencer and forwards all
// inbound session events (from session.Receive()) to DeltaOutbox.
// When config is non-nil, a SESSION.UPDATE message is sent once to an unmarked
// session immediately after its first SESSION.OPEN or SESSION.CREATED event
// is received from the provider.
// Provider sessions that already sent their initial configuration during
// ConnectSession opt out through the optional InitialSessionConfigSent marker.
// User input is admitted through EnqueueSessionInput; contentful audio
// arriving while the model is streaming triggers barge-in (RESPONSE.CANCEL).
func NewSessionModelRunner(si messages.SessionInferencer, bufferCapacity int, config *messages.SessionUpdateConfig) *ModelRunner {
	return &ModelRunner{
		sessionInferencer: si,
		sessionConfig:     config,
		Inbox:             messages.NewTypedBuffer[messages.InferenceRequest](bufferCapacity),
		DeltaOutbox:       messages.NewTypedBuffer[messages.StreamMessage](bufferCapacity),
		ingress:           newSessionIngress(),
	}
}

// CancelCurrentExecution cancels the per-execution context for the inference
// that is currently in flight. The runner's outer goroutine continues running and
// will block on the next Inbox.ReadBlocking call; only the active request is failed.
// Safe to call from any goroutine; no-op when no inference is in flight.
func (r *ModelRunner) CancelCurrentExecution() {
	r.execMu.Lock()
	defer r.execMu.Unlock()
	if r.execCancel != nil {
		r.execCancel()
	}
}

func (r *ModelRunner) Run(ctx context.Context) error {
	if r.sessionInferencer != nil {
		return r.runSession(ctx)
	}
	return r.runInference(ctx)
}

// SetClock sets the clock that times held onset audio. Call it before Run.
func (r *ModelRunner) SetClock(source clock.TimerSource) { r.clock = source }

func (r *ModelRunner) timerSource() clock.TimerSource {
	if r.clock != nil {
		return r.clock
	}
	return clock.Real{}
}

// Local barge-in.
//
// So that one loud transient (a cough, a door) does not cancel the response,
// frames that could be a barge-in are held until onset is decided: speech
// sends the cancel and then releases them, so the cancel still precedes the
// interrupting audio at the provider (the live barge-in contract) at a cost
// of at most MinSpeech of input latency; a transient is released unchanged
// once a quiet frame follows it.
//
// When the provider runs turn detection (server VAD) its speech_started stops
// local playback and truncates the heard item, so the runner's cancel only
// stops generation (KeepPlayback). Otherwise the runner owns playback: its
// cancel stops local playback, and when no response is active but its audio
// is still playing (the provider delivers faster than real time, so
// response.done arrives while seconds remain audible) speech interrupts local
// playback directly.

// BargeInConfig tunes local barge-in detection.
type BargeInConfig = sessionstate.BargeInConfig

// DefaultBargeInConfig returns the default detector tuning.
func DefaultBargeInConfig() BargeInConfig { return sessionstate.DefaultBargeInConfig() }

// SetBargeInConfig replaces the local barge-in tuning. Call it before Run.
func (r *ModelRunner) SetBargeInConfig(config BargeInConfig) { r.bargeInConfig = &config }

func (r *ModelRunner) bargeInTuning() BargeInConfig {
	if r.bargeInConfig != nil {
		return *r.bargeInConfig
	}
	return DefaultBargeInConfig()
}

// forwardSessionAudio admits one user audio frame: interrupting audio first
// goes through local barge-in, then any held onset frames and the frame
// itself reach the provider in order.
//
// A response that is still non-terminal is a barge-in target, including the
// interval between response creation and its first output delta. The bound
// tool continuation is deliberately excluded: nothing re-requests a
// cancelled tool continuation, so its obligation would be left permanently
// unresolved (a room participant died exactly so, 557 ms into its
// continuation, having produced no audio). A response that was already playing
// when the continuation was queued is not the continuation -- the request is
// deferred until that response ends -- so it stays interruptible.
func (r *ModelRunner) forwardSessionAudio(ctx context.Context, session messages.Session, state *sessionRunState, input messages.SessionAudioInput) error {
	if sessionAdmissionClosed(session) {
		// Room-bound shutdown closes input admission before it cancels the
		// session. A frame that was already queued behind that boundary is
		// intentionally discarded without manufacturing a provider failure.
		return nil
	}
	if input.InterruptionPolicy.InterruptsResponse() {
		held, err := r.bargeIn(ctx, session, input.PCM, state)
		if held || err != nil {
			return err
		}
	}
	if err := r.releaseHeldAudio(ctx, session, state); err != nil {
		return err
	}
	return forwardUserAudio(ctx, session, input.PCM)
}

// bargeIn applies local barge-in to one interrupting frame and reports
// whether it holds the frame while onset is undecided.
func (r *ModelRunner) bargeIn(ctx context.Context, session messages.Session, pcm []byte, state *sessionRunState) (bool, error) {
	providerVAD := providerOwnsTurnDetection(session)
	playback, playing := localPlayback(session)
	tuning := r.bargeInTuning()
	onset, loud := state.Onset.Detector.Observe(pcm, playing, inputSampleRate(session), tuning)
	cancelTarget := state.CancelTarget()
	switch {
	case onset && cancelTarget:
		return false, r.sendBargeInCancel(ctx, session, state, providerVAD)
	case onset && !providerVAD && !state.ResponseActive() && playing.Active:
		playback.InterruptLocalPlayback(ctx)
	case loud && !onset && cancelTarget:
		state.Onset.Hold(pcm, r.timerSource(), tuning.MinSpeech)
		return true, nil
	}
	return false, nil
}

// releaseHeldAudio forwards onset frames held while barge-in was undecided.
// Once admission has closed (room shutdown) they are discarded, like any
// frame that reaches that boundary.
func (r *ModelRunner) releaseHeldAudio(ctx context.Context, session messages.Session, state *sessionRunState) error {
	held := state.Onset.Take()
	if sessionAdmissionClosed(session) {
		return nil
	}
	for _, pcm := range held {
		if err := forwardUserAudio(ctx, session, pcm); err != nil {
			return err
		}
	}
	return nil
}

// flushHeldAudio releases held onset audio ahead of a control input or a
// response boundary. A send failure is published as the runner's terminal
// audio failure.
func (r *ModelRunner) flushHeldAudio(ctx context.Context, session messages.Session, state *sessionRunState) {
	if err := r.releaseHeldAudio(ctx, session, state); err != nil {
		r.publishSessionAudioFailure(err, state.Response.HasOutput)
	}
}

func forwardUserAudio(ctx context.Context, session messages.Session, pcm []byte) error {
	outcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue(pcm),
	})
	if !outcome.OK() {
		return sessionAudioSendError("audio", outcome)
	}
	return nil
}

func (r *ModelRunner) sendBargeInCancel(ctx context.Context, session messages.Session, state *sessionRunState, keepPlayback bool) error {
	value := messages.NewResponseCancelValue()
	value.KeepPlayback = keepPlayback
	cancelOutcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: value,
	})
	if !cancelOutcome.OK() {
		return sessionAudioSendError("response cancel", cancelOutcome)
	}
	// Keep the response in flight until its terminal MESSAGE.END arrives,
	// but never send a second cancel for more audio belonging to the same
	// response.
	state.NoteCancelSent()
	return nil
}

func providerOwnsTurnDetection(session messages.Session) bool {
	detector, ok := session.(messages.SessionTurnDetection)
	return ok && detector.ProviderTurnDetection()
}

func inputSampleRate(session messages.Session) int {
	if format, ok := session.(messages.SessionInputFormat); ok {
		return format.InputAudioSampleRate()
	}
	return 0
}

func localPlayback(session messages.Session) (messages.SessionLocalPlayback, messages.LocalPlaybackState) {
	playback, ok := session.(messages.SessionLocalPlayback)
	if !ok {
		return nil, messages.LocalPlaybackState{}
	}
	return playback, playback.LocalPlayback()
}
