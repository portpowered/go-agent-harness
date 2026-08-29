package participants

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// ModelRunner runs inference asynchronously as an active participant.
// It reads InferenceRequest from its inbox, calls the Inferencer, and writes
// StreamMessage deltas to DeltaOutbox as they arrive. Message assembly (accumulating
// deltas into a full Message) is performed by GlobalOrdering in the engine, not here.
//
// Streaming: each StreamMessage from InferStream is forwarded to DeltaOutbox with
// additive terminal metadata on terminal events when the provider omitted it.
// On MESSAGE.END or ERROR the stream is considered complete.
// If the channel closes without MESSAGE.END a provider-close MESSAGE.END is emitted
// so the ordering layer always sees a complete boundary.
//
// Non-streaming fallback: if InferStream returns nil or an error, Infer is called and
// synthetic deltas (MESSAGE.START → content deltas → MESSAGE.END) are emitted to
// DeltaOutbox so the same assembly path is taken as for streaming.
//
// Each delta is tagged with runner-specific ordering: ActorStreamID, ActorProvidedIndex,
// ActorProvidedID, ActorID (see ORDERING.md). streamID and actorIndex are set at the
// start of each inference and incremented for each delta.
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
func (r *ModelRunner) runSession(ctx context.Context) error {
	session, err := r.sessionInferencer.ConnectSession(ctx)
	if err != nil {
		return fmt.Errorf("session connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	responseInFlight := false // true after MESSAGE.START until MESSAGE.END from the model
	responseCancelSent := false
	sessionClosed := false
	hasOutput := false
	responseCompleted := false
	var pendingSendErrors []messages.StreamMessage
	awaitingToolContinuationResponse := false
	suppressNextToolContinuation := false

	for {
		// User audio and control events are queued by the session loop after
		// it observes a completed response. Give those pending inputs a
		// deterministic transport turn before accepting the next inbound
		// provider event, so a scheduled audio turn cannot be interleaved
		// ahead of its own commit/response.create boundary.
		handled, closed, audioErr := r.forwardPendingSessionInputs(ctx, session, &responseInFlight, &responseCancelSent, &pendingSendErrors, &awaitingToolContinuationResponse, &suppressNextToolContinuation)
		if audioErr != nil {
			r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
			return audioErr
		}
		if closed {
			r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
			return nil
		}
		if handled {
			continue
		}
		select {
		case <-ctx.Done():
			r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
			return ctx.Err()
		case <-session.Done():
			for {
				msg, ok := session.Receive().Read()
				if !ok {
					break
				}
				responseInFlight, responseCancelSent, sessionClosed, hasOutput, responseCompleted = r.forwardSessionMessage(ctx, session, msg, responseInFlight, responseCancelSent, sessionClosed, hasOutput, responseCompleted)
				if msg.Type == messages.StreamTypeMessageEnd && awaitingToolContinuationResponse {
					r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
					pendingSendErrors = nil
					awaitingToolContinuationResponse = false
				}
			}
			r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
			if !sessionClosed {
				terminalProvenance := messages.TerminalProvenanceProvider
				terminalOutputState := outputState(hasOutput)
				if responseCompleted {
					// Preserve the existing session teardown contract after a
					// completed response. A transport close before any response
					// boundary remains provider-authored and uses observed output.
					terminalProvenance = messages.TerminalProvenanceSession
					terminalOutputState = messages.TerminalOutputNotApplicable
				}
				r.DeltaOutbox.Write(ctx, messages.StreamMessage{
					Type: messages.StreamTypeSessionClose,
					Value: messages.NewSessionCloseValueWithTerminal(
						"",
						"provider_closed",
						"transport",
						messages.TerminalReasonProviderClose,
						terminalProvenance,
						terminalOutputState,
					),
				})
			}
			return nil
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				return nil
			}
			if err := r.forwardSessionAudio(ctx, session, pcm, &responseInFlight, &responseCancelSent); err != nil {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				return err
			}
		case evt, ok := <-r.UserEventInbox:
			if !ok {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				return nil
			}
			if evt.Type == messages.StreamTypeResponseCreate && suppressNextToolContinuation {
				// A result in this batch was rejected at the provider boundary. Do
				// not ask the provider to continue from a partially delivered batch;
				// the accepted sibling remains pending and the deferred result error
				// names the rejected call when the session reaches a terminal path.
				suppressNextToolContinuation = false
				r.sessionToolContinuation = sessionToolContinuationSuppressed
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				pendingSendErrors = nil
				continue
			}
			if err := r.drainSessionAudio(ctx, session, &responseInFlight, &responseCancelSent); err != nil {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				return err
			}
			if failure, deferred, responseAccepted := r.forwardSessionEvent(ctx, session, evt); deferred {
				pendingSendErrors = append(pendingSendErrors, failure)
				if evt.Type == messages.StreamTypeToolCallEnd {
					suppressNextToolContinuation = true
					r.sessionToolContinuation = sessionToolContinuationSuppressed
				}
				if responseAccepted {
					awaitingToolContinuationResponse = true
					r.sessionToolContinuation = sessionToolContinuationAccepted
				}
			} else {
				if responseAccepted {
					awaitingToolContinuationResponse = true
					r.sessionToolContinuation = sessionToolContinuationAccepted
				} else if failure.Type != "" && evt.Type == messages.StreamTypeResponseCreate {
					r.sessionToolContinuation = sessionToolContinuationSuppressed
				}
			}
		case req, ok := <-r.Inbox.Chan():
			if !ok {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				return nil
			}
			r.sendLatestUserText(ctx, session, req)
		case msg, ok := <-session.Receive().Chan():
			if !ok {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				return nil
			}
			if msg.Type == messages.StreamTypeSessionClose {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				pendingSendErrors = nil
				awaitingToolContinuationResponse = false
				r.sessionToolContinuation = sessionToolContinuationNone
			}
			responseInFlight, responseCancelSent, sessionClosed, hasOutput, responseCompleted = r.forwardSessionMessage(ctx, session, msg, responseInFlight, responseCancelSent, sessionClosed, hasOutput, responseCompleted)
			if msg.Type == messages.StreamTypeMessageEnd && awaitingToolContinuationResponse {
				r.flushPendingSessionSendErrors(ctx, pendingSendErrors)
				pendingSendErrors = nil
				awaitingToolContinuationResponse = false
			}
		}
	}
}

// forwardPendingSessionInputs gives queued user audio/control messages
// priority over the provider receive path. It returns whether it forwarded
// anything and whether either user inbox was closed.
func (r *ModelRunner) forwardPendingSessionInputs(ctx context.Context, session messages.Session, responseInFlight, responseCancelSent *bool, pendingSendErrors *[]messages.StreamMessage, awaitingToolContinuationResponse *bool, suppressNextToolContinuation *bool) (handled, closed bool, audioErr error) {
	for {
		select {
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return true, true, nil
			}
			if err := r.forwardSessionAudio(ctx, session, pcm, responseInFlight, responseCancelSent); err != nil {
				return true, false, err
			}
			handled = true
		case evt, ok := <-r.UserEventInbox:
			if !ok {
				return true, true, nil
			}
			if evt.Type == messages.StreamTypeResponseCreate && suppressNextToolContinuation != nil && *suppressNextToolContinuation {
				// See the main session-event branch: one rejected result makes the
				// batch continuation invalid, even when another result was accepted.
				*suppressNextToolContinuation = false
				r.sessionToolContinuation = sessionToolContinuationSuppressed
				if pendingSendErrors != nil {
					r.flushPendingSessionSendErrors(ctx, *pendingSendErrors)
					*pendingSendErrors = nil
				}
				handled = true
				continue
			}
			if err := r.drainSessionAudio(ctx, session, responseInFlight, responseCancelSent); err != nil {
				return true, false, err
			}
			if failure, deferred, responseAccepted := r.forwardSessionEvent(ctx, session, evt); deferred && pendingSendErrors != nil {
				*pendingSendErrors = append(*pendingSendErrors, failure)
				if evt.Type == messages.StreamTypeToolCallEnd && suppressNextToolContinuation != nil {
					*suppressNextToolContinuation = true
					r.sessionToolContinuation = sessionToolContinuationSuppressed
				}
				if responseAccepted && awaitingToolContinuationResponse != nil {
					*awaitingToolContinuationResponse = true
					r.sessionToolContinuation = sessionToolContinuationAccepted
				}
			} else {
				if responseAccepted && awaitingToolContinuationResponse != nil {
					*awaitingToolContinuationResponse = true
					r.sessionToolContinuation = sessionToolContinuationAccepted
				} else if failure.Type != "" && evt.Type == messages.StreamTypeResponseCreate {
					r.sessionToolContinuation = sessionToolContinuationSuppressed
				}
			}
			handled = true
		default:
			return handled, false, nil
		}
	}
}

func (r *ModelRunner) drainSessionAudio(ctx context.Context, session messages.Session, responseInFlight, responseCancelSent *bool) error {
	for {
		select {
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return nil
			}
			if err := r.forwardSessionAudio(ctx, session, pcm, responseInFlight, responseCancelSent); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (r *ModelRunner) forwardSessionAudio(ctx context.Context, session messages.Session, pcm []byte, responseInFlight, responseCancelSent *bool) error {
	// Barge-in: new user audio while the current model response is still
	// non-terminal. The response-created-before-first-audio state is
	// intentionally included: provider response creation and its first output
	// delta are separate ordered events, and speech in that interval must not
	// be mistaken for an idle session.
	if *responseInFlight && !*responseCancelSent && hasPCM16Signal(pcm) {
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
		*responseCancelSent = true
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

func sessionAudioSendError(operation string, outcome messages.SessionSendOutcome) error {
	if outcome.Err != nil {
		return fmt.Errorf("session %s send failed with status %q: %w", operation, outcome.Status, outcome.Err)
	}
	return fmt.Errorf("session %s send failed with status %q", operation, outcome.Status)
}

// forwardSessionEvent preserves the legacy best-effort behavior for ordinary
// user events, but turns a rejected tool-result or continuation send into an
// observable stream error. The session lifecycle can then report the still-
// unresolved obligation instead of allowing a false clean close.
func (r *ModelRunner) forwardSessionEvent(ctx context.Context, session messages.Session, msg messages.StreamMessage) (messages.StreamMessage, bool, bool) {
	outcome := messages.SendSessionWithOutcome(ctx, session, msg)
	if outcome.OK() {
		return messages.StreamMessage{}, false, msg.Type == messages.StreamTypeResponseCreate
	}
	callID := ""
	classification := "unresolved_tool_result"
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		callID = value.ToolCallID
	}
	message := fmt.Sprintf("tool result %q was not delivered: session send status %q", callID, outcome.Status)
	if msg.Type == messages.StreamTypeResponseCreate {
		classification = "unresolved_tool_continuation"
		message = fmt.Sprintf("tool continuation was not requested: session send status %q", outcome.Status)
	}
	if msg.Type != messages.StreamTypeToolCallEnd && msg.Type != messages.StreamTypeResponseCreate {
		return messages.StreamMessage{}, false, false
	}
	value := messages.NewErrorValueWithTerminal(
		message,
		classification,
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputNone,
	)
	value.Err = outcome.Err
	failure := messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: value,
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		// A batch may contain another result that was accepted. Keep this
		// failure until the batch's continuation boundary so the caller can
		// suppress that invalid continuation and then report every remaining
		// per-call obligation together.
		return failure, true, false
	}
	r.DeltaOutbox.Write(ctx, failure)
	return messages.StreamMessage{}, false, false
}

func (r *ModelRunner) flushPendingSessionSendErrors(ctx context.Context, failures []messages.StreamMessage) {
	for _, failure := range failures {
		r.DeltaOutbox.Write(ctx, failure)
	}
}

func (r *ModelRunner) forwardSessionMessage(ctx context.Context, session messages.Session, msg messages.StreamMessage, responseInFlight bool, responseCancelSent bool, sessionClosed bool, hasOutput bool, responseCompleted bool) (bool, bool, bool, bool, bool) {
	// Track the provider response lifecycle for barge-in detection. A response
	// is live from MESSAGE.START through MESSAGE.END; audio start/end alone do
	// not define its terminal boundary.
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		// hasOutput and responseCompleted describe the current response, not
		// the lifetime of the persistent session. Reset them at each response
		// boundary so a later disconnect is classified from the latest turn.
		hasOutput = false
		responseCompleted = false
		responseInFlight = true
		responseCancelSent = false
	case messages.StreamTypeAudioStart:
		// Some compatible sessions omit MESSAGE.START around an audio stream;
		// AUDIO.START is still enough to establish a live response for the
		// existing session contract.
		if !responseInFlight {
			responseCancelSent = false
		}
		responseInFlight = true
	case messages.StreamTypeMessageEnd:
		responseInFlight = false
		if responseCancelSent {
			// Realtime providers normally acknowledge RESPONSE.CANCEL with a
			// response.done event. Preserve that wire boundary so the next
			// input can proceed, but mark it as interrupted rather than a
			// normally completed assistant turn.
			if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
				outputState := messages.TerminalOutputNone
				if hasOutput {
					outputState = messages.TerminalOutputPartial
				}
				msg.Value = messages.NewMessageEndValueWithTerminal(
					value.Usage,
					messages.TerminalReasonPartialOutput,
					messages.TerminalProvenanceLoop,
					outputState,
				)
			}
			responseCompleted = false
		} else {
			responseCompleted = true
		}
	case messages.StreamTypeSessionClose:
		sessionClosed = true
		msg = normalizeSessionCloseMessage(msg)
	}
	// A provider may have already queued output when RESPONSE.CANCEL reaches
	// it. The wire adapter cannot retract those frames, but they must not cross
	// the customer-facing session boundary after the local cancellation. Keep
	// MESSAGE.END so the cancelled response can still close and the next turn
	// can be admitted.
	if responseCancelSent && isCustomerOutputDelta(msg) {
		return responseInFlight, responseCancelSent, sessionClosed, hasOutput, responseCompleted
	}
	if isOutputDelta(msg) {
		hasOutput = true
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
	return responseInFlight, responseCancelSent, sessionClosed, hasOutput, responseCompleted
}

func normalizeSessionCloseMessage(msg messages.StreamMessage) messages.StreamMessage {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return msg
	}
	if value.TerminalReason == "" {
		if value.Reason == "provider_closed" {
			value.TerminalReason = messages.TerminalReasonProviderClose
		} else {
			value.TerminalReason = messages.TerminalReasonSessionClose
		}
	}
	if value.Classification == "" {
		// The gateway public taxonomy classifies a provider transport close
		// without completion as transport; clean session closes keep their
		// descriptive reason.
		if value.TerminalReason == messages.TerminalReasonProviderClose {
			value.Classification = "transport"
		} else {
			value.Classification = string(value.TerminalReason)
		}
	}
	if value.TerminalProvenance == "" {
		value.TerminalProvenance = messages.TerminalProvenanceSession
	}
	if value.OutputState == "" {
		value.OutputState = messages.TerminalOutputNotApplicable
	}
	return msg
}

func (r *ModelRunner) sendLatestUserText(ctx context.Context, session messages.Session, req messages.InferenceRequest) {
	// A rich tool result is the newest conversation entry after the coordinator
	// schedules the follow-up inference. Sessions that support complete
	// messages (for example, multimodal realtime providers) must receive that
	// result before the next response is requested; otherwise an image tool
	// result would remain only in loop history and never reach the provider.
	switch r.sendLatestSessionToolResults(ctx, session, req.Messages) {
	case sessionToolResultsComplete:
		return
	case sessionToolResultsFlatFallback:
		// The flat fallback sends one explicit RESPONSE.CREATE after the
		// correlated result batch. Do not inject the previous user text, which
		// would create a duplicate or ungrounded response.
		return
	case sessionToolResultsAlreadyForwarded:
		// Text-only results are delivered by ToolResultForwarder before this
		// result-driven inference request reaches the session runner. Consume
		// that boundary when the session loop recorded it; an isolated caller
		// still needs the explicit request below.
		if r.sessionToolContinuation != sessionToolContinuationNone {
			r.sessionToolContinuation = sessionToolContinuationNone
			return
		}
		if !r.sendLatestUserTextOnly(ctx, session, req.Messages) {
			r.requestSessionResponse(ctx, session)
		}
		return
	case sessionToolResultsFailed:
		// A complete-message send may have partially reached the provider. Do
		// not send an unrelated user-text fallback that could request another
		// response or duplicate the batch.
		return
	}

	if !r.sendLatestUserTextOnly(ctx, session, req.Messages) && sessionHasToolResultSuffix(req.Messages) {
		r.requestSessionResponse(ctx, session)
	}
}

func (r *ModelRunner) sendLatestUserTextOnly(ctx context.Context, session messages.Session, history []messages.Message) bool {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != messages.RoleUser {
			continue
		}
		text := msg.TextContent()
		if text == "" {
			return false
		}
		session.Send(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue(text),
		})
		return true
	}
	return false
}

func sessionHasToolResultSuffix(history []messages.Message) bool {
	return len(history) > 0 && history[len(history)-1].Role == messages.RoleTool
}

func (r *ModelRunner) requestSessionResponse(ctx context.Context, session messages.Session) {
	// Replays and legacy injected sessions do not expose this optional
	// capability, so their captured provider traffic remains unchanged.
	messages.RequestSessionResponse(ctx, session)
}

// sessionMessageSender is an optional extension to the stream-only Session
// contract. Keeping the assertion local preserves compatibility with existing
// session implementations while allowing providers with a complete-message
// path to deliver rich tool results.
type sessionMessageSender interface {
	SendMessage(context.Context, messages.Message) bool
}

type sessionMessageWithoutResponseSender interface {
	SendMessageWithoutResponse(context.Context, messages.Message) bool
}

// sessionMessageCapabilities lets adapters that preserve the optional method
// set report whether the wrapped provider actually supports complete messages.
// Without this capability bit, a stream-only session wrapper can look like a
// complete-message session because its forwarding methods necessarily return
// false when the underlying session has no such path.
type sessionMessageCapabilities interface {
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

type sessionToolResultDelivery uint8

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
func (r *ModelRunner) sendLatestSessionToolResults(ctx context.Context, session messages.Session, history []messages.Message) sessionToolResultDelivery {
	first := len(history)
	for first > 0 && history[first-1].Role == messages.RoleTool {
		first--
	}
	if first == len(history) {
		return sessionToolResultsNotFound
	}
	toolResults := history[first:]
	if !sessionToolResultsContainImage(toolResults) {
		// Text-only results are forwarded by ToolResultForwarder in the same
		// tick as the coordinator's request. The forwarder also emits the
		// single explicit continuation boundary, so this request must not send
		// the original user text a second time.
		return sessionToolResultsAlreadyForwarded
	}

	sender, hasCompleteMessagePath := session.(sessionMessageSender)
	withoutResponse, canDeferResponse := session.(sessionMessageWithoutResponseSender)
	if capabilities, ok := session.(sessionMessageCapabilities); ok {
		hasCompleteMessagePath = hasCompleteMessagePath && capabilities.SupportsCompleteMessages()
		canDeferResponse = canDeferResponse && capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	if !hasCompleteMessagePath || (len(toolResults) > 1 && !canDeferResponse) {
		if !sendSessionToolResultsAsStream(ctx, session, history, toolResults) {
			return sessionToolResultsFailed
		}
		return sessionToolResultsFlatFallback
	}

	for index, result := range toolResults {
		last := index == len(toolResults)-1
		if !last && canDeferResponse {
			if !withoutResponse.SendMessageWithoutResponse(ctx, result) {
				return sessionToolResultsFailed
			}
			continue
		}
		if !sender.SendMessage(ctx, result) {
			return sessionToolResultsFailed
		}
	}
	return sessionToolResultsComplete
}

// sendSessionToolResultsAsStream is the explicit compatibility fallback for
// sessions that cannot accept complete messages. It preserves one correlated
// TOOLCALL.END per result, followed by one explicit RESPONSE.CREATE. Image
// bytes cannot be represented by the stream-only contract, so they are
// intentionally not claimed as delivered on this path.
func sendSessionToolResultsAsStream(ctx context.Context, session messages.Session, history, results []messages.Message) bool {
	for _, result := range results {
		if result.ToolCallID == "" {
			return false
		}
		if !session.Send(ctx, messages.StreamMessage{
			Type: messages.StreamTypeToolCallEnd,
			Value: messages.NewToolCallEndValue(
				result.ToolCallID,
				sessionToolResultName(history, result),
				result.TextContent(),
			),
		}) {
			return false
		}
	}
	return session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func sessionToolResultName(history []messages.Message, result messages.Message) string {
	if result.Name != "" {
		return result.Name
	}
	for i := len(history) - 1; i >= 0; i-- {
		for _, call := range history[i].ToolCalls {
			if call.ID == result.ToolCallID && call.Name != "" {
				return call.Name
			}
		}
	}
	return ""
}

func sessionToolResultsContainImage(results []messages.Message) bool {
	for _, result := range results {
		for _, part := range result.ContentParts {
			if _, ok := part.(messages.ImagePart); ok {
				return true
			}
		}
	}
	return false
}

func (r *ModelRunner) runInference(ctx context.Context) error {
	for {
		req, ok := r.Inbox.ReadBlocking(ctx.Done())
		if !ok {
			return ctx.Err()
		}

		execCtx, execCancel := context.WithCancel(ctx)
		r.execMu.Lock()
		r.execCancel = execCancel
		r.execMu.Unlock()

		r.streamID = mustStreamID("model")
		r.actorIndex = 0
		r.currentPassID = req.LoopPassID

		streamCh, err := r.inferencer.InferStream(execCtx, req)
		if isCancellationError(err) {
			r.emitSyntheticDeltas(ctx, messages.InferenceResult{}, err)
		} else if err == nil && streamCh != nil {
			r.drainStream(ctx, execCtx, streamCh)
		} else {
			// The inference provider does not support streaming; fall back to non-streaming
			// and emit synthetic deltas so the ordering layer sees the same delta boundary.
			result, inferErr := r.inferencer.Infer(execCtx, req)
			r.emitSyntheticDeltas(ctx, result, inferErr)
		}

		r.execMu.Lock()
		r.execCancel = nil
		r.execMu.Unlock()
		execCancel()
	}
}

// writeDelta assigns runner-specific ordering (ActorStreamID, ActorProvidedIndex, ActorProvidedID, ActorID, LoopPassID) and writes to DeltaOutbox.
func (r *ModelRunner) writeDelta(ctx context.Context, sm messages.StreamMessage) {
	sm.ActorStreamID = r.streamID
	sm.ActorProvidedIndex = r.actorIndex
	sm.ActorProvidedID = fmt.Sprintf("model-%s-%d", r.streamID, r.actorIndex)
	sm.ActorID = messages.Model
	sm.LoopPassID = r.currentPassID
	r.actorIndex++
	r.DeltaOutbox.Write(ctx, sm)
}

// drainStream forwards each StreamMessage from the inferencer channel to DeltaOutbox.
// It stops on MESSAGE.END, ERROR, or channel close (emitting a synthetic MESSAGE.END in
// the latter case so the ordering layer always sees a complete message boundary).
func (r *ModelRunner) drainStream(writeCtx, execCtx context.Context, ch <-chan messages.StreamMessage) {
	hasOutput := false
	for {
		if err := execCtx.Err(); err != nil {
			r.writeDelta(writeCtx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Role:  messages.RoleAssistant,
				Value: cancellationErrorValue(err, messages.TerminalProvenanceLoop, outputState(hasOutput)),
			})
			return
		}
		select {
		case <-writeCtx.Done():
			return
		case <-execCtx.Done():
			r.writeDelta(writeCtx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Role:  messages.RoleAssistant,
				Value: cancellationErrorValue(execCtx.Err(), messages.TerminalProvenanceLoop, outputState(hasOutput)),
			})
			return
		case msg, ok := <-ch:
			if !ok {
				if err := execCtx.Err(); err != nil {
					r.writeDelta(writeCtx, messages.StreamMessage{
						Type:  messages.StreamTypeError,
						Role:  messages.RoleAssistant,
						Value: cancellationErrorValue(err, messages.TerminalProvenanceLoop, outputState(hasOutput)),
					})
					return
				}
				// Channel closed without MESSAGE.END; emit a provider-close end so
				// callers can distinguish transport close from clean completion.
				r.writeDelta(writeCtx, messages.StreamMessage{
					Type: messages.StreamTypeMessageEnd,
					Role: messages.RoleAssistant,
					Value: messages.NewMessageEndValueWithTerminal(
						messages.TokenUsage{},
						messages.TerminalReasonProviderClose,
						messages.TerminalProvenanceProvider,
						outputState(hasOutput),
					),
				})
				return
			}
			msg = normalizeProviderTerminalMessage(msg, hasOutput)
			r.writeDelta(writeCtx, msg)
			if isOutputDelta(msg) {
				hasOutput = true
			}
			switch msg.Value.(type) {
			case *messages.MessageEndValue, *messages.ErrorValue:
				return
			}
		}
	}
}

// emitSyntheticDeltas converts a non-streaming InferenceResult into the same sequence
// of deltas that a streaming response would produce, so the ordering layer's assembly
// path is identical regardless of whether the inferencer supports streaming.
func (r *ModelRunner) emitSyntheticDeltas(ctx context.Context, result messages.InferenceResult, inferErr error) {
	if inferErr != nil {
		if isCancellationError(inferErr) {
			r.writeDelta(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: cancellationErrorValue(inferErr, messages.TerminalProvenanceLoop, messages.TerminalOutputNone),
			})
			return
		}
		r.writeDelta(ctx, messages.StreamMessage{
			Type: messages.StreamTypeError,
			Value: messages.NewErrorValueWithTerminal(
				inferErr.Error(),
				"",
				messages.TerminalReasonTerminalFailure,
				messages.TerminalProvenanceLoop,
				messages.TerminalOutputNone,
			),
		})
		return
	}

	r.writeDelta(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	})

	// Emit text content.
	if text := result.Message.TextContent(); result.Message.HasText() {
		r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()})
		if text != "" {
			r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)})
		}
		r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()})
	}

	// Emit tool call deltas so the ordering layer can assemble ToolCalls on the message.
	for _, tc := range result.ToolCalls {
		r.writeDelta(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewToolCallStartValue(tc.ID, tc.Name),
		})
		r.writeDelta(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewToolCallEndValue(tc.ID, tc.Name, tc.Arguments),
		})
	}

	// Emit multimodal content parts.
	for _, part := range result.Message.ContentParts {
		switch p := part.(type) {
		case messages.AudioPart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()})
			}
		case messages.ImagePart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeImageStart, Value: messages.NewImageStartValue(p.MediaType)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeImageDelta, Value: messages.NewImageDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeImageEnd, Value: messages.NewImageEndValue()})
			}
		case messages.VideoPart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeVideoStart, Value: messages.NewVideoStartValue(p.MediaType)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeVideoDelta, Value: messages.NewVideoDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeVideoEnd, Value: messages.NewVideoEndValue()})
			}
		case messages.FilePart:
			if len(p.Bytes) > 0 {
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeFileStart, Value: messages.NewFileStartValue(p.MediaType, p.Name)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeFileDelta, Value: messages.NewFileDeltaValue(p.Bytes)})
				r.writeDelta(ctx, messages.StreamMessage{Type: messages.StreamTypeFileEnd, Value: messages.NewFileEndValue()})
			}
		}
	}

	// Emit token usage information if present.
	if result.TokenUsage.PromptTokens != 0 || result.TokenUsage.CompletionTokens != 0 || result.TokenUsage.TotalTokens != 0 || result.TokenUsage.ReasoningTokens != 0 {
		r.writeDelta(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeUsageInfo,
			Value: messages.NewUsageInfoValue(result.TokenUsage),
		})
	}

	r.writeDelta(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewSynthesizedMessageEndValue(result.TokenUsage),
	})
}

func normalizeProviderTerminalMessage(msg messages.StreamMessage, hasOutput bool) messages.StreamMessage {
	switch value := msg.Value.(type) {
	case *messages.MessageEndValue:
		if value.TerminalReason == "" {
			value.TerminalReason = messages.TerminalReasonProviderAuthoredCompletion
		}
		if value.TerminalProvenance == "" {
			value.TerminalProvenance = messages.TerminalProvenanceProvider
		}
		if value.OutputState == "" {
			value.OutputState = messages.TerminalOutputComplete
		}
	case *messages.ErrorValue:
		if value.TerminalReason == "" {
			value.TerminalReason = messages.TerminalReasonTerminalFailure
		}
		if value.TerminalProvenance == "" {
			value.TerminalProvenance = messages.TerminalProvenanceProvider
		}
		if value.OutputState == "" {
			value.OutputState = outputState(hasOutput)
		}
	}
	return msg
}

func outputState(hasOutput bool) messages.TerminalOutputState {
	if hasOutput {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNone
}

// hasPCM16Signal distinguishes a real input frame from the zero-filled
// cadence frames produced by a room mixer while no participant is speaking.
// The frame is still forwarded in either case so the provider's audio timing
// and VAD state remain intact; only a frame with at least one non-zero byte can
// be the user activity that cancels an in-flight response.
func hasPCM16Signal(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return true
		}
	}
	return false
}

func cancellationErrorValue(err error, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) *messages.ErrorValue {
	if err == nil {
		err = context.Canceled
	}
	value := messages.NewErrorValueWithTerminal(
		err.Error(),
		string(messages.TerminalReasonCancellation),
		messages.TerminalReasonCancellation,
		provenance,
		outputState,
	)
	value.Err = err
	return value
}

func isCancellationError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isOutputDelta(msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeToolCallDelta,
		messages.StreamTypeToolCallEnd,
		messages.StreamTypeRefusal:
		return true
	default:
		return false
	}
}

func isCustomerOutputDelta(msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeTextDelta,
		messages.StreamTypeReasoningDelta,
		messages.StreamTypeAudioDelta,
		messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta,
		messages.StreamTypeFileDelta,
		messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeTranscriptDelta,
		messages.StreamTypeRefusal:
		return true
	default:
		// Tool-call deltas remain visible after a speech cancellation so the
		// tool lifecycle can resolve or reject the outstanding call explicitly.
		return false
	}
}
