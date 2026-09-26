package participants

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"strings"
	"sync"
)

type ModelRunner struct {
	inferencer        messages.Inferencer
	sessionInferencer messages.SessionInferencer
	sessionConfig     *messages.SessionUpdateConfig // sent as SESSION.UPDATE on the first SESSION.OPEN or SESSION.CREATED
	Inbox             *messages.TypedBuffer[messages.InferenceRequest]
	DeltaOutbox       *messages.TypedBuffer[messages.StreamMessage]
	// UserAudioInbox receives raw PCM. Contentful frames cancel an active
	// response before forwarding; silence passes through. Direct writes use the
	// interrupting-by-default policy, and admitted slices are retained by the runner.
	UserAudioInbox chan []byte
	// UserEventInbox receives pre-built outbound StreamMessages from the user
	// side in session mode. Each message is forwarded to the provider session
	// unchanged, preserving caller ordering. It carries control-plane turns
	// such as MESSAGE.END (input_audio_buffer.commit + response.create on the
	// OpenAI Realtime wire).
	UserEventInbox chan messages.StreamMessage

	// sessionInputInbox is the single ordered ingress for the explicit session
	// helper API. Keeping audio and control events in one bounded queue prevents
	// a later audio frame from overtaking the MESSAGE.END that delimits the
	// preceding turn. UserAudioInbox and UserEventInbox remain available as
	// legacy direct-input paths and are intentionally independent.
	sessionInputInbox chan sessionInput

	// sessionInputMu establishes a FIFO boundary for the public session input
	// helpers. Audio and control events are admitted to one bounded ingress in
	// caller order and then forwarded by the session runner in that order.
	sessionInputMu sync.Mutex
	ingressStop    sessionIngressStop // closed when runSession returns; releases parked waiting admissions
	cancelLane     sessionCancelLane  // lets RESPONSE.CANCEL overtake queued bulk audio

	streamID      string // set at start of each inference (one stream per request)
	actorIndex    int    // incremented for each delta written to DeltaOutbox
	currentPassID int    // LoopPassID from the current InferenceRequest

	// sessionToolContinuation records the result of the session-loop's explicit
	// tool-result boundary. It lets the request-driven compatibility helper
	// distinguish a continuation already queued by ToolResultForwarder from an
	// isolated caller that still needs to request one.
	sessionToolContinuation sessionToolContinuationState

	// sessionToolEventMu protects the count of tool-result boundary events that
	// have been accepted into the ordered session ingress but not yet consumed by the
	// session runner. The coordinator can enqueue the follow-up inference
	// request immediately after the forwarder returns, so the count closes the
	// race where that request would otherwise send a bare RESPONSE.CREATE
	// before the queued TOOLCALL.END and continuation.
	sessionToolEventMu       sync.Mutex
	pendingSessionToolEvents int

	execMu     sync.Mutex
	execCancel context.CancelFunc // cancel for the current per-execution context; nil when idle
}

// ErrSessionInputQueueFull reports that the explicit session ingress is at
// capacity. Returning this bounded admission result keeps callers such as tool
// result forwarding from waiting on a provider or an unbounded queue.
var ErrSessionInputQueueFull = errors.New("session input queue is full")

type sessionToolContinuationState uint8

const (
	sessionToolContinuationNone sessionToolContinuationState = iota
	sessionToolContinuationAccepted
	sessionToolContinuationSuppressed
)

// sessionRunState is the mutable lifecycle state owned by one persistent
// session runner. Keeping the provider response state together with the
// pending tool-result bookkeeping lets the pending-input preflight observe an
// already-queued provider boundary before it admits user audio.
type sessionRunState struct {
	responseInFlight     bool
	responseCancelSent   bool
	sessionClosed        bool
	hasOutput            bool
	responseCompleted    bool
	pendingSendErrors    []messages.StreamMessage
	suppressContinuation bool
	// continuationRequested records that the provider accepted a tool
	// continuation RESPONSE.CREATE whose response has not started yet. The
	// request is only sent while no response is active, so the next response
	// the provider opens is that continuation.
	continuationRequested bool
	// continuationInFlight marks the current response as the tool
	// continuation. Only this response is exempt from barge-in; every other
	// response, including one that was already playing when the continuation
	// was queued, stays interruptible.
	continuationInFlight       bool
	continuationResponseID     string
	continuationEnded          bool
	continuationCreate         messages.StreamMessage
	currentResponseID          string
	cancelledResponseIDs       responseIDSet
	retiredResponseIDs         responseIDSet
	terminalResponseIDs        responseIDSet
	acknowledgementOutstanding bool
	acknowledgementStart       *messages.StreamMessage
	acknowledgementCancelled   bool
	acknowledgementEnded       bool
	deferredSessionEvents      []messages.StreamMessage
	initialSessionConfigSent   bool
}

// sessionResponseState is retained as an alias for the identity-aware helper
// methods; all session lifecycle fields remain owned by one persistent state.
type sessionResponseState = sessionRunState

func newSessionResponseState() *sessionResponseState {
	return &sessionResponseState{}
}

func responseID(value string) string {
	return strings.TrimSpace(value)
}

// ensureMaps is retained for callers; the bounded identity sets need no
// initialization.
func (s *sessionRunState) ensureMaps() {}

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
// The Inbox is allocated but not read in session mode.
// When config is non-nil, a SESSION.UPDATE message is sent once to an unmarked
// session immediately after its first SESSION.OPEN or SESSION.CREATED event
// is received from the provider.
// Provider sessions that already sent their initial configuration during
// ConnectSession opt out through the optional InitialSessionConfigSent marker.
// UserAudioInbox is a buffered channel for accepting raw PCM audio input;
// contentful audio arriving while the model is streaming triggers barge-in
// (RESPONSE.CANCEL). Silence frames continue to reach the provider unchanged.
func NewSessionModelRunner(si messages.SessionInferencer, bufferCapacity int, config *messages.SessionUpdateConfig) *ModelRunner {
	return &ModelRunner{
		sessionInferencer: si,
		sessionConfig:     config,
		Inbox:             messages.NewTypedBuffer[messages.InferenceRequest](bufferCapacity),
		DeltaOutbox:       messages.NewTypedBuffer[messages.StreamMessage](bufferCapacity),
		UserAudioInbox:    make(chan []byte, 64),
		UserEventInbox:    make(chan messages.StreamMessage, 8),
		sessionInputInbox: make(chan sessionInput, 72),
		cancelLane:        sessionCancelLane{inbox: make(chan messages.StreamMessage, sessionCancelLaneCapacity)},
	}
}

func (r *ModelRunner) enqueueSessionAudioInput(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy, operation string) error {
	if r == nil || r.sessionInputInbox == nil {
		return fmt.Errorf("%s: not in session mode", operation)
	}
	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.enqueueSessionAudioInputLocked(ctx, pcm, policy, false)
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

// runSession connects a persistent session and forwards all inbound events from
// session.Receive() to DeltaOutbox. It runs until the context is cancelled or
// the session terminates. This is the session-mode counterpart to runInference.
//
// When sessionConfig is set, a SESSION.UPDATE message is sent once to an
// unmarked session immediately after its first SESSION.OPEN or SESSION.CREATED
// event is received (before forwarding it to DeltaOutbox). Provider-owned
// initial configuration is not echoed.
//
// When UserAudioInbox is set, this method also selects on it. If audio arrives
// while the model has a non-terminal response (from MESSAGE.START through
// MESSAGE.END), RESPONSE.CANCEL is sent to the session first (barge-in), then
// the audio is forwarded.
func (r *ModelRunner) forwardPendingSessionMessages(ctx context.Context, session messages.Session, state *sessionRunState) (handled bool) {
	for {
		msg, ok := session.Receive().Read()
		if !ok {
			return handled
		}
		r.forwardSessionMessageState(ctx, session, state, msg)
		handled = true
	}
}

func (r *ModelRunner) forwardSessionMessageState(ctx context.Context, session messages.Session, state *sessionRunState, msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeSessionClose {
		r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
		state.pendingSendErrors = nil
		clearSessionContinuation(state)
		r.sessionToolContinuation = sessionToolContinuationNone
	}
	messageEnded := r.forwardSessionMessageWithState(ctx, session, msg, state)
	acknowledgementEnded := state.acknowledgementEnded
	if acknowledgementEnded {
		state.acknowledgementEnded = false
	}
	if acknowledgementEnded || messageEnded {
		// Either this response's own terminal boundary was just observed, or
		// an outstanding acknowledgement was just finalized (possibly by a
		// replacement response retiring it before its own MESSAGE.END could
		// be owned). Either way, the response that deferred events were
		// waiting on is no longer active, so it is now safe to replay them.
		r.flushDeferredSessionEvents(ctx, session, state)
	}
	if state.continuationEnded {
		state.continuationEnded = false
		r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
		state.pendingSendErrors = nil
	}
}

func (r *ModelRunner) drainSessionAudioWithState(ctx context.Context, session messages.Session, state *sessionResponseState) error {
	state.ensureMaps()
	for {
		select {
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return nil
			}
			if err := r.forwardSessionAudioWithState(ctx, session, pcm, state); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (r *ModelRunner) forwardSessionAudioWithState(ctx context.Context, session messages.Session, pcm []byte, state *sessionResponseState) error {
	return r.forwardSessionAudioWithPolicyWithState(ctx, session, pcm, messages.SessionAudioInputPolicyDefault, state)
}

func (r *ModelRunner) forwardSessionAudioWithPolicyWithState(ctx context.Context, session messages.Session, pcm []byte, policy messages.SessionAudioInputPolicy, state *sessionResponseState) error {
	state.ensureMaps()
	if sessionAdmissionClosed(session) {
		// Room-bound shutdown closes input admission before it cancels the
		// session. A frame that was already queued behind that boundary is
		// intentionally discarded without manufacturing a provider failure.
		return nil
	}
	// Barge-in: new user audio while the current model response is still
	// non-terminal. The response-created-before-first-audio state is
	// intentionally included: provider response creation and its first output
	// delta are separate ordered events, and speech in that interval must not
	// be mistaken for an idle session.
	//
	// The response that is itself the requested continuation of an already
	// accepted tool result (state.continuationInFlight) is deliberately
	// excluded. Nothing re-requests a cancelled tool continuation: its
	// MESSAGE.END is rewritten with TerminalReasonPartialOutput and the tool's
	// obligation is left permanently unresolved, so the session later fails
	// closed with an unresolved tool_continuation. A room participant observed
	// this exactly: one peer audio frame 557ms into its tool continuation
	// response cancelled it and the participant died having produced no audio.
	//
	// The exemption is scoped to that one response. A response that was
	// already playing when the continuation was queued (for example a
	// server-VAD reply) is not the continuation: the continuation request is
	// deferred until that response ends, so interrupting it strands nothing.
	if policy.InterruptsResponse() && (state.responseInFlight || state.acknowledgementOutstanding) && !state.continuationInFlight && !state.responseCancelSent && hasPCM16Signal(pcm) {
		cancelOutcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
			Type:  messages.StreamTypeResponseCancel,
			Value: messages.NewAutomaticResponseCancelValue(),
		})
		if !cancelOutcome.OK() {
			return sessionAudioSendError("response cancel", cancelOutcome)
		}
		// Keep the response in flight until its terminal MESSAGE.END arrives,
		// but never send a second cancel for more audio belonging to the same
		// response.
		state.responseCancelSent = true
		if state.acknowledgementOutstanding {
			state.acknowledgementCancelled = true
		}
		if state.currentResponseID != "" {
			state.cancelledResponseIDs.add(state.currentResponseID)
		}
	}
	// Forward the user audio to the inference provider.
	audioOutcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue(pcm),
	})
	if !audioOutcome.OK() {
		return sessionAudioSendError("audio", audioOutcome)
	}
	return nil
}

func (r *ModelRunner) forwardSessionMessageWithState(ctx context.Context, session messages.Session, msg messages.StreamMessage, state *sessionResponseState) bool {
	state.ensureMaps()
	acknowledgementResponse := r.tagSessionAcknowledgement(ctx, state, &msg)
	msgID := responseID(msg.ResponseID)
	messageEndOwned := false

	// Track the provider response lifecycle for barge-in detection. A response
	// is live from MESSAGE.START through MESSAGE.END; audio start/end alone do
	// not define its terminal boundary. When a provider starts a replacement
	// response before the older one has drained, the older response is retired
	// and can no longer mutate the current lifecycle.
	r.retrySessionContinuationOnRejection(ctx, session, state, msg)
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		startSessionResponse(state, msgID, acknowledgementResponse)
		tagSessionContinuation(state, &msg, msgID)
	case messages.StreamTypeMessageEnd:
		tagSessionContinuation(state, &msg, msgID)
		messageEndOwned = endSessionResponse(state, &msg, msgID, acknowledgementResponse)
	case messages.StreamTypeSessionClose:
		state.sessionClosed = true
		msg = normalizeSessionCloseMessage(msg)
	default:
		tagSessionContinuation(state, &msg, msgID)
	}
	// A provider may have already queued output when RESPONSE.CANCEL reaches
	// it. The wire adapter cannot retract those frames, but they must not cross
	// the customer-facing session boundary after the local cancellation. Keep
	// MESSAGE.END so the cancelled response can still close and the next turn
	// can be admitted. An identified event is admitted only for its current
	// response owner; an old terminal event cannot clear a replacement.
	if staleSessionCustomerOutput(state, msg) {
		return messageEndOwned
	}
	if isOutputDelta(msg) {
		state.hasOutput = true
	}
	r.forwardInitialSessionConfig(ctx, session, state, msg)
	r.DeltaOutbox.Write(ctx, msg)
	return messageEndOwned
}

func isSessionResponseStreamType(typ messages.StreamMessageType) bool {
	switch typ {
	case messages.StreamTypeMessageStart,
		messages.StreamTypeMessageEnd,
		messages.StreamTypeTextStart,
		messages.StreamTypeTextDelta,
		messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioStart,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeAudioEnd,
		messages.StreamTypeImageStart,
		messages.StreamTypeImageDelta,
		messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart,
		messages.StreamTypeFileDelta,
		messages.StreamTypeFileEnd,
		messages.StreamTypeReasoningStart,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeReasoningEnd,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTranscriptEnd,
		messages.StreamTypeRefusal,
		messages.StreamTypeUsageInfo:
		return true
	default:
		return false
	}
}

const (
	sessionToolResultsNotFound sessionToolResultDelivery = iota
	sessionToolResultsComplete
	sessionToolResultsFlatFallback
	sessionToolResultsAlreadyForwarded
	sessionToolResultsFailed
)

// sendLatestSessionToolResults sends the contiguous tool-result suffix from
// one inference request. Tool results are emitted as one batch, so preserving
// their order is important for providers that associate each result with its
// originating call. The final result requests the next model response; any
// preceding results use the provider's no-response variant when available.
//
// A batch containing an image is either delivered wholly through the complete
// message path or wholly through the flat TOOLCALL.END fallback. Keeping that
// decision at batch scope prevents a text sibling from being delivered twice,
// and ensures stream-only sessions do not silently lose rich results.
