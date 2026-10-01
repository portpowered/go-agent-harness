package participants

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Session tool-result delivery: how a result-driven inference request from
// the coordinator reaches the provider session.

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
			// A parked waiting admission of that boundary also counts here.
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
	case sessionToolResultsNotFound:
		if !r.sendLatestUserTextOnly(ctx, session, req.Messages) && sessionHasToolResultSuffix(req.Messages) {
			r.requestSessionResponse(ctx, session)
		}
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

	canDeferResponse := messages.SupportsSessionMessagesWithoutResponse(session)
	if !messages.SupportsSessionMessages(session) || (len(toolResults) > 1 && !canDeferResponse) {
		if !sendSessionToolResultsAsStream(ctx, session, history, toolResults) {
			return sessionToolResultsFailed
		}
		return sessionToolResultsFlatFallback
	}

	return sendCompleteSessionToolResults(ctx, session, canDeferResponse, toolResults)
}

func sendCompleteSessionToolResults(ctx context.Context, session messages.Session, canDeferResponse bool, toolResults []messages.Message) sessionToolResultDelivery {
	for index, result := range toolResults {
		last := index == len(toolResults)-1
		if !last && canDeferResponse {
			if !messages.SendSessionMessageWithoutResponse(ctx, session, result) {
				return sessionToolResultsFailed
			}
			continue
		}
		if !messages.SendSessionMessage(ctx, session, result) {
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
		Value: messages.NewToolContinuationResponseCreateValue(),
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
