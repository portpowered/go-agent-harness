package participants

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
		if r.hasPendingSessionToolEvents() {
			// ToolResultForwarder has accepted the result boundary into the
			// session input queue, but the session loop has not forwarded it to
			// the provider yet. Waiting here preserves TOOLCALL.END before the
			// continuation even when the inference request wins the runner's
			// select race.
			return
		}
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
		// A message with an explicit TextPart is a valid user turn even when
		// its text is empty. This distinction is required by replay, where an
		// explicitly recorded empty prompt must still produce its captured
		// conversation.item.create frame. A message with no text part remains
		// ineligible for this text-only fallback.
		if !msg.HasText() {
			return false
		}
		text := msg.TextContent()
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

	return sendCompleteSessionToolResults(ctx, sender, withoutResponse, canDeferResponse, toolResults)
}

func sendCompleteSessionToolResults(ctx context.Context, sender sessionMessageSender, withoutResponse sessionMessageWithoutResponseSender, canDeferResponse bool, toolResults []messages.Message) sessionToolResultDelivery {
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

type sessionInputKind uint8

const (
	sessionInputAudio sessionInputKind = iota + 1
	sessionInputEvent
	sessionInputMessage
)

type sessionInput struct {
	kind            sessionInputKind
	audio           messages.SessionAudioInput
	event           messages.StreamMessage
	message         messages.Message
	requestResponse bool
}

// EnqueueSessionMessage queues one complete user message in the same bounded
// ingress as PCM and control events. Complete-message providers use this path
// for rich opening turns such as image content; requestResponse controls
// whether the provider starts a response immediately or waits for a later
// audio commit.
func (r *ModelRunner) EnqueueSessionMessage(ctx context.Context, msg messages.Message, requestResponse bool) error {
	if r == nil || r.sessionInputInbox == nil {
		return fmt.Errorf("EnqueueSessionMessage: not in session mode")
	}
	r.sessionInputMu.Lock()
	defer r.sessionInputMu.Unlock()
	if ctx == nil {
		return fmt.Errorf("EnqueueSessionMessage: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.sessionInputInbox <- sessionInput{kind: sessionInputMessage, message: msg, requestResponse: requestResponse}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrSessionInputQueueFull
	}
}

func (r *ModelRunner) forwardSessionCompleteMessage(ctx context.Context, session messages.Session, msg messages.Message, requestResponse bool) error {
	if requestResponse {
		sender, ok := session.(sessionMessageSender)
		if !ok {
			return errors.New("session does not support complete messages")
		}
		if !sender.SendMessage(ctx, msg) {
			return errors.New("session rejected complete message")
		}
		return nil
	}
	sender, ok := session.(sessionMessageWithoutResponseSender)
	if !ok {
		return errors.New("session does not support complete messages without response")
	}
	if !sender.SendMessageWithoutResponse(ctx, msg) {
		return errors.New("session rejected complete message without response")
	}
	return nil
}

// forwardSessionInput dispatches the ordered ingress without reading a second
// input or changing the provider lifecycle observation order.
func (r *ModelRunner) forwardSessionInput(ctx context.Context, session messages.Session, state *sessionRunState, input sessionInput) error {
	switch input.kind {
	case sessionInputAudio:
		return r.forwardSessionAudioInputWithState(ctx, session, input.audio, state)
	case sessionInputEvent:
		r.forwardQueuedSessionEvent(ctx, session, state, input.event)
	case sessionInputMessage:
		return r.forwardSessionCompleteMessage(ctx, session, input.message, input.requestResponse)
	}
	return nil
}

func (r *ModelRunner) noteAcceptedSessionResponse(state *sessionRunState, evt messages.StreamMessage) {
	if isToolAcknowledgementResponseCreate(evt) {
		state.acknowledgementOutstanding = true
		state.acknowledgementCancelled = false
		state.responseCancelSent = false
		return
	}
	state.awaitingContinuation = true
	r.sessionToolContinuation = sessionToolContinuationAccepted
}

func (r *ModelRunner) noteAcceptedSessionCancel(state *sessionRunState) {
	// Live hosts send explicit cancellation through the same ordered control
	// path as audio admission. Match the automatic barge-in state so late
	// untagged provider deltas cannot escape from the cancelled response.
	state.responseCancelSent = true
	if state.acknowledgementOutstanding {
		state.acknowledgementCancelled = true
	}
	if state.currentResponseID != "" {
		state.cancelledResponseIDs[state.currentResponseID] = struct{}{}
	}
}

func (r *ModelRunner) noteDeferredSessionFailure(state *sessionRunState, evt, failure messages.StreamMessage) {
	if failure.Type != "" {
		state.pendingSendErrors = append(state.pendingSendErrors, failure)
	}
	if evt.Type == messages.StreamTypeToolCallEnd {
		state.suppressContinuation = true
		r.sessionToolContinuation = sessionToolContinuationSuppressed
	}
}

func isToolAcknowledgementResponseCreate(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeResponseCreate {
		return false
	}
	value, ok := msg.Value.(*messages.ResponseCreateValue)
	return ok && value.IsToolAcknowledgement()
}
