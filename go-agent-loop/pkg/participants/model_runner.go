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
	sessionConfig     *messages.SessionUpdateConfig // sent as SESSION.UPDATE on SESSION.CREATED
	Inbox             *messages.TypedBuffer[messages.InferenceRequest]
	DeltaOutbox       *messages.TypedBuffer[messages.StreamMessage]
	// UserAudioInbox receives raw PCM audio frames from the user in session mode.
	// When contentful audio arrives while the model is streaming an audio
	// response, the model runner sends RESPONSE.CANCEL (barge-in) before
	// forwarding the audio. Zero-filled cadence frames are forwarded without
	// cancelling the response. Direct writes to this legacy channel use the
	// interrupting-by-default policy. An accepted PCM slice is retained by the
	// runner; the caller must not modify it after admission.
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
	responseInFlight           bool
	responseCancelSent         bool
	sessionClosed              bool
	hasOutput                  bool
	responseCompleted          bool
	pendingSendErrors          []messages.StreamMessage
	awaitingContinuation       bool
	suppressContinuation       bool
	currentResponseID          string
	cancelledResponseIDs       map[string]struct{}
	retiredResponseIDs         map[string]struct{}
	terminalResponseIDs        map[string]struct{}
	acknowledgementOutstanding bool
	acknowledgementCancelled   bool
	acknowledgementEnded       bool
	deferredSessionEvents      []messages.StreamMessage
}

// sessionResponseState is retained as an alias for the identity-aware helper
// methods; all session lifecycle fields remain owned by one persistent state.
type sessionResponseState = sessionRunState

func newSessionResponseState() *sessionResponseState {
	return &sessionResponseState{
		cancelledResponseIDs: make(map[string]struct{}),
		retiredResponseIDs:   make(map[string]struct{}),
		terminalResponseIDs:  make(map[string]struct{}),
	}
}

func responseID(value string) string {
	return strings.TrimSpace(value)
}

func (s *sessionRunState) ensureMaps() {
	if s.cancelledResponseIDs == nil {
		s.cancelledResponseIDs = make(map[string]struct{})
	}
	if s.retiredResponseIDs == nil {
		s.retiredResponseIDs = make(map[string]struct{})
	}
	if s.terminalResponseIDs == nil {
		s.terminalResponseIDs = make(map[string]struct{})
	}
}

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
// When config is non-nil, a SESSION.UPDATE message is sent to the session
// immediately after SESSION.CREATED is received from the provider.
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
	}
}

// EnqueueSessionAudioInput queues raw PCM for the session with the legacy
// interrupting-by-default policy. An accepted PCM slice is retained by the
// runner; the caller must not modify it after admission.
func (r *ModelRunner) EnqueueSessionAudioInput(ctx context.Context, pcm []byte) error {
	return r.enqueueSessionAudioInput(ctx, pcm, messages.SessionAudioInputPolicyDefault, "EnqueueSessionAudioInput")
}

// EnqueueSessionAudioInputWithPolicy queues raw PCM with an explicit
// interruption policy in the same ordered ingress used by session events.
// An accepted PCM slice is retained and must not be modified by the caller.
func (r *ModelRunner) EnqueueSessionAudioInputWithPolicy(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	return r.enqueueSessionAudioInput(ctx, pcm, policy, "EnqueueSessionAudioInputWithPolicy")
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
	select {
	case r.sessionInputInbox <- sessionInput{kind: sessionInputAudio, audio: messages.SessionAudioInput{PCM: pcm, InterruptionPolicy: policy}}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrSessionInputQueueFull
	}
}

// EnqueueSessionEvent queues a control-plane event in the same ordered ingress
// as audio admitted by EnqueueSessionAudioInput.
func (r *ModelRunner) EnqueueSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	if r == nil || r.sessionInputInbox == nil {
		return fmt.Errorf("EnqueueSessionEvent: not in session mode")
	}

	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.markSessionToolEventQueued(msg)
	select {
	case r.sessionInputInbox <- sessionInput{kind: sessionInputEvent, event: msg}:
		return nil
	case <-ctx.Done():
		r.markSessionToolEventConsumed(msg)
		return ctx.Err()
	default:
		r.markSessionToolEventConsumed(msg)
		return ErrSessionInputQueueFull
	}
}

func isSessionToolEvent(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeToolCallEnd || msg.Type == messages.StreamTypeResponseCreate
}

func (r *ModelRunner) markSessionToolEventQueued(msg messages.StreamMessage) {
	if !isSessionToolEvent(msg) {
		return
	}
	r.sessionToolEventMu.Lock()
	r.pendingSessionToolEvents++
	r.sessionToolEventMu.Unlock()
}

func (r *ModelRunner) markSessionToolEventConsumed(msg messages.StreamMessage) {
	if !isSessionToolEvent(msg) {
		return
	}
	r.sessionToolEventMu.Lock()
	if r.pendingSessionToolEvents > 0 {
		r.pendingSessionToolEvents--
	}
	r.sessionToolEventMu.Unlock()
}

func (r *ModelRunner) hasPendingSessionToolEvents() bool {
	r.sessionToolEventMu.Lock()
	defer r.sessionToolEventMu.Unlock()
	return r.pendingSessionToolEvents > 0
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
// When sessionConfig is set, a SESSION.UPDATE message is sent to the session
// immediately after SESSION.CREATED is received (before forwarding it to DeltaOutbox).
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
		state.awaitingContinuation = false
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
	if messageEnded && state.awaitingContinuation && !acknowledgementEnded {
		r.flushPendingSessionSendErrors(ctx, state.pendingSendErrors)
		state.pendingSendErrors = nil
		state.awaitingContinuation = false
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
	// A response that is itself the requested continuation of an already
	// accepted tool result (state.awaitingContinuation) is deliberately
	// excluded. Unlike an ordinary spoken response or a tool acknowledgement,
	// nothing re-requests a cancelled tool continuation: MESSAGE.END for a
	// cancelled response is rewritten with TerminalReasonPartialOutput and no
	// further response.create is ever queued for that call, so the tool's
	// obligation is left permanently unresolved and the session later fails
	// closed with an unresolved tool_continuation -- even though the
	// interrupting audio was ordinary room/peer input, not a deliberate
	// interrupt of this participant's own turn. A room participant observed
	// this exactly: it received one peer audio frame with signal 557ms into
	// its tool continuation response, sent RESPONSE.CANCEL against it,
	// and died with classification=tool_continuation at t=2.9s having never
	// produced a single sample of its own audio (sent.pcm was 0 bytes). Held
	// off, the continuation is free to complete and deliver the tool result;
	// the participant's next ordinary response remains fully interruptible.
	if policy.InterruptsResponse() && (state.responseInFlight || state.acknowledgementOutstanding) && !state.awaitingContinuation && !state.responseCancelSent && hasPCM16Signal(pcm) {
		cancelOutcome := messages.SendSessionWithOutcome(ctx, session, messages.StreamMessage{
			Type:  messages.StreamTypeResponseCancel,
			Value: messages.NewResponseCancelValue(),
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
			state.cancelledResponseIDs[state.currentResponseID] = struct{}{}
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
	if msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		state.acknowledgementOutstanding = true
	}
	acknowledgementResponse := state.acknowledgementOutstanding
	if acknowledgementResponse && isSessionResponseStreamType(msg.Type) {
		msg.ResponsePurpose = messages.ResponsePurposeToolAcknowledgement
	}
	msgID := responseID(msg.ResponseID)
	messageEndOwned := false

	// Track the provider response lifecycle for barge-in detection. A response
	// is live from MESSAGE.START through MESSAGE.END; audio start/end alone do
	// not define its terminal boundary. When a provider starts a replacement
	// response before the older one has drained, the older response is retired
	// and can no longer mutate the current lifecycle.
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		if beginSessionResponse(state, msgID) {
			state.hasOutput = false
			state.responseCompleted = false
			state.responseCancelSent = false
			state.responseInFlight = true
			if acknowledgementResponse && state.acknowledgementCancelled {
				state.responseCancelSent = true
				if msgID != "" {
					state.cancelledResponseIDs[msgID] = struct{}{}
				}
			}
		}
	case messages.StreamTypeMessageEnd:
		if ownsSessionResponseEnd(state, msgID) {
			ownedID := msgID
			if ownedID == "" {
				ownedID = state.currentResponseID
			}
			state.responseInFlight = false
			if state.responseCancelSent {
				// Realtime providers normally acknowledge RESPONSE.CANCEL with a
				// response.done event. Preserve that wire boundary so the next
				// input can proceed, but mark it as interrupted rather than a
				// normally completed assistant turn.
				if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
					outputState := messages.TerminalOutputNone
					if state.hasOutput {
						outputState = messages.TerminalOutputPartial
					}
					msg.Value = messages.NewMessageEndValueWithTerminal(
						value.Usage,
						messages.TerminalReasonPartialOutput,
						messages.TerminalProvenanceLoop,
						outputState,
					)
				}
				state.responseCompleted = false
			} else if acknowledgementResponse {
				// A progress acknowledgement is never the assistant turn that
				// satisfies a user input or a tool continuation.
				state.responseCompleted = false
			} else {
				state.responseCompleted = true
			}
			if ownedID != "" {
				state.terminalResponseIDs[ownedID] = struct{}{}
			}
			state.currentResponseID = ""
			messageEndOwned = true
			if acknowledgementResponse {
				state.acknowledgementOutstanding = false
				state.acknowledgementCancelled = false
				state.acknowledgementEnded = true
				state.responseCancelSent = false
				state.hasOutput = false
			}
		}
	case messages.StreamTypeSessionClose:
		state.sessionClosed = true
		msg = normalizeSessionCloseMessage(msg)
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
	// On SESSION.CREATED, send back SESSION.UPDATE with the configured
	// session parameters (model, instructions, modalities) if set. Use the
	// same failure-preserving path as mid-session updates so a rejected
	// provider update cannot silently leave the advertised surface stale.
	if msg.Type == messages.StreamTypeSessionCreated && r.sessionConfig != nil {
		r.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type:  messages.StreamTypeSessionUpdate,
			Value: messages.NewSessionUpdateValue(r.sessionConfig),
		})
	}
	r.DeltaOutbox.Write(ctx, msg)
	return messageEndOwned
}

func beginSessionResponse(state *sessionResponseState, msgID string) bool {
	if msgID != "" {
		if _, retired := state.retiredResponseIDs[msgID]; retired {
			return false
		}
		if _, terminal := state.terminalResponseIDs[msgID]; terminal {
			return false
		}
	}
	if state.responseInFlight {
		if state.currentResponseID == msgID {
			// Duplicate starts for the same response must not reset cancellation
			// or output state.
			return false
		}
		if state.currentResponseID != "" && msgID == "" {
			// An untagged start cannot claim an identified response.
			return false
		}
		if state.currentResponseID != "" && msgID != state.currentResponseID {
			state.retiredResponseIDs[state.currentResponseID] = struct{}{}
			// The retired response's own MESSAGE.END, whenever it eventually
			// arrives, will never be "owned" again (ownsSessionResponseEnd
			// rejects retired ids), so the reset that normally happens there
			// would never run. Finalize any acknowledgement bookkeeping for it
			// here instead of leaving acknowledgementOutstanding stuck true:
			// left stuck, every later non-silent audio frame would look like a
			// live barge-in target (state.responseInFlight || state.
			// acknowledgementOutstanding) even once nothing is actually
			// active, producing a RESPONSE.CANCEL the provider rejects with
			// response_cancel_not_active.
			if state.acknowledgementOutstanding {
				state.acknowledgementOutstanding = false
				state.acknowledgementCancelled = false
				state.acknowledgementEnded = true
			}
		}
	}
	state.currentResponseID = msgID
	return true
}

func ownsSessionResponseEnd(state *sessionResponseState, msgID string) bool {
	if msgID != "" {
		if _, terminal := state.terminalResponseIDs[msgID]; terminal {
			return false
		}
		if _, retired := state.retiredResponseIDs[msgID]; retired {
			return false
		}
		if _, cancelled := state.cancelledResponseIDs[msgID]; cancelled && state.currentResponseID != msgID {
			return false
		}
		if state.responseInFlight {
			return msgID == "" || state.currentResponseID == msgID
		}
		// A response.done without response.created is accepted once for
		// compatibility with providers that omit the opening event.
		return !state.responseCompleted
	}
	if state.responseInFlight {
		// Compatible providers may omit response_id on response.done. The
		// sole active identified response owns that terminal event unless a
		// non-empty competing ID is supplied.
		return true
	}
	return !state.responseCompleted
}

func staleSessionCustomerOutput(state *sessionResponseState, msg messages.StreamMessage) bool {
	if !isCustomerOutputDelta(msg) {
		return false
	}
	msgID := responseID(msg.ResponseID)
	if msgID != "" {
		if _, cancelled := state.cancelledResponseIDs[msgID]; cancelled {
			return true
		}
		if _, retired := state.retiredResponseIDs[msgID]; retired {
			return true
		}
		if _, terminal := state.terminalResponseIDs[msgID]; terminal {
			return true
		}
		if state.currentResponseID != "" && state.currentResponseID != msgID {
			return true
		}
		return false
	}
	return state.responseCancelSent
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
