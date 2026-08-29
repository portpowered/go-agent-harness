package participants

import (
	"context"
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
