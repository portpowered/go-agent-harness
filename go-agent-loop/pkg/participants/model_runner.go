package participants

import (
	"context"
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
	// cancelling the response.
	UserAudioInbox chan []byte
	// UserEventInbox receives pre-built outbound StreamMessages from the user
	// side in session mode. Each message is forwarded to the provider session
	// unchanged, preserving caller ordering. It carries control-plane turns
	// such as MESSAGE.END (input_audio_buffer.commit + response.create on the
	// OpenAI Realtime wire).
	UserEventInbox chan messages.StreamMessage

	// sessionInputMu establishes a FIFO boundary for the public session input
	// helpers. A control event sent after audio must not enter the event inbox
	// until the earlier audio has reached the provider.
	sessionInputMu    sync.Mutex
	audioInputMu      sync.Mutex
	audioInputPending int
	audioInputDrained chan struct{}

	streamID      string // set at start of each inference (one stream per request)
	actorIndex    int    // incremented for each delta written to DeltaOutbox
	currentPassID int    // LoopPassID from the current InferenceRequest

	// sessionToolContinuation records the result of the session-loop's explicit
	// tool-result boundary. It lets the request-driven compatibility helper
	// distinguish a continuation already queued by ToolResultForwarder from an
	// isolated caller that still needs to request one.
	sessionToolContinuation sessionToolContinuationState

	execMu     sync.Mutex
	execCancel context.CancelFunc // cancel for the current per-execution context; nil when idle
}

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
	awaitingContinuation bool
	suppressContinuation bool
	currentResponseID    string
	cancelledResponseIDs map[string]struct{}
	retiredResponseIDs   map[string]struct{}
	terminalResponseIDs  map[string]struct{}
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
	}
}

// EnqueueSessionAudioInput queues raw PCM for the session and records it as
// pending until the session runner forwards it to the provider. The pending
// state lets EnqueueSessionEvent preserve ordering across the two input
// channels without changing the legacy exported channels used by callers and
// tests.
func (r *ModelRunner) EnqueueSessionAudioInput(ctx context.Context, pcm []byte) error {
	if r == nil || r.UserAudioInbox == nil {
		return fmt.Errorf("EnqueueSessionAudioInput: not in session mode")
	}

	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	r.markAudioInputPending()
	select {
	case r.UserAudioInbox <- pcm:
		return nil
	case <-ctx.Done():
		r.completeAudioInput()
		return ctx.Err()
	}
}

// EnqueueSessionEvent queues a control-plane event after all audio enqueued by
// an earlier EnqueueSessionAudioInput call has been forwarded to the provider.
func (r *ModelRunner) EnqueueSessionEvent(ctx context.Context, msg messages.StreamMessage) error {
	if r == nil || r.UserEventInbox == nil {
		return fmt.Errorf("EnqueueSessionEvent: not in session mode")
	}

	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	if err := r.waitForAudioInput(ctx); err != nil {
		return err
	}
	select {
	case r.UserEventInbox <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *ModelRunner) markAudioInputPending() {
	r.audioInputMu.Lock()
	defer r.audioInputMu.Unlock()
	if r.audioInputPending == 0 {
		r.audioInputDrained = make(chan struct{})
	}
	r.audioInputPending++
}

func (r *ModelRunner) completeAudioInput() {
	r.audioInputMu.Lock()
	defer r.audioInputMu.Unlock()
	if r.audioInputPending == 0 {
		return
	}
	r.audioInputPending--
	if r.audioInputPending == 0 {
		close(r.audioInputDrained)
		r.audioInputDrained = nil
	}
}

func (r *ModelRunner) waitForAudioInput(ctx context.Context) error {
	for {
		r.audioInputMu.Lock()
		pending := r.audioInputPending
		drained := r.audioInputDrained
		r.audioInputMu.Unlock()
		if pending == 0 {
			return nil
		}
		select {
		case <-drained:
		case <-ctx.Done():
			return ctx.Err()
		}
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
	if messageEnded && state.awaitingContinuation {
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
	defer r.completeAudioInput()
	state.ensureMaps()
	// Barge-in: new user audio while the current model response is still
	// non-terminal. The response-created-before-first-audio state is
	// intentionally included: provider response creation and its first output
	// delta are separate ordered events, and speech in that interval must not
	// be mistaken for an idle session.
	if state.responseInFlight && !state.responseCancelSent && hasPCM16Signal(pcm) {
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
			} else {
				state.responseCompleted = true
			}
			if ownedID != "" {
				state.terminalResponseIDs[ownedID] = struct{}{}
			}
			state.currentResponseID = ""
			messageEndOwned = true
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
	// session parameters (model, instructions, modalities) if set.
	if msg.Type == messages.StreamTypeSessionCreated && r.sessionConfig != nil {
		session.Send(ctx, messages.StreamMessage{
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
