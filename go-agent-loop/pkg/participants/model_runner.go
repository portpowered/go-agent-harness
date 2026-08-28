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
	// When audio arrives while the model is streaming an audio response, the
	// model runner sends RESPONSE.CANCEL (barge-in) before forwarding the audio.
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

	execMu     sync.Mutex
	execCancel context.CancelFunc // cancel for the current per-execution context; nil when idle
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
// audio arriving while the model is streaming triggers barge-in (RESPONSE.CANCEL).
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
// while the model is streaming an audio response (between AUDIO.START and AUDIO.END),
// RESPONSE.CANCEL is sent to the session first (barge-in), then the audio is forwarded.
func (r *ModelRunner) runSession(ctx context.Context) error {
	session, err := r.sessionInferencer.ConnectSession(ctx)
	if err != nil {
		return fmt.Errorf("session connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	audioStreaming := false // true between AUDIO.START and AUDIO.END from the model
	sessionClosed := false
	hasOutput := false
	responseCompleted := false

	for {
		// User audio and control events are queued by the session loop after
		// it observes a completed response. Give those pending inputs a
		// deterministic transport turn before accepting the next inbound
		// provider event, so a scheduled audio turn cannot be interleaved
		// ahead of its own commit/response.create boundary.
		handled, closed := r.forwardPendingSessionInputs(ctx, session, &audioStreaming)
		if closed {
			return nil
		}
		if handled {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-session.Done():
			for {
				msg, ok := session.Receive().Read()
				if !ok {
					break
				}
				audioStreaming, sessionClosed, hasOutput, responseCompleted = r.forwardSessionMessage(ctx, session, msg, audioStreaming, sessionClosed, hasOutput, responseCompleted)
			}
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
				return nil
			}
			r.forwardSessionAudio(ctx, session, pcm, &audioStreaming)
		case evt, ok := <-r.UserEventInbox:
			if !ok {
				return nil
			}
			r.drainSessionAudio(ctx, session, &audioStreaming)
			r.forwardSessionEvent(ctx, session, evt)
		case req, ok := <-r.Inbox.Chan():
			if !ok {
				return nil
			}
			r.sendLatestUserText(ctx, session, req)
		case msg, ok := <-session.Receive().Chan():
			if !ok {
				return nil
			}
			audioStreaming, sessionClosed, hasOutput, responseCompleted = r.forwardSessionMessage(ctx, session, msg, audioStreaming, sessionClosed, hasOutput, responseCompleted)
		}
	}
}

// forwardPendingSessionInputs gives queued user audio/control messages
// priority over the provider receive path. It returns whether it forwarded
// anything and whether either user inbox was closed.
func (r *ModelRunner) forwardPendingSessionInputs(ctx context.Context, session messages.Session, audioStreaming *bool) (handled, closed bool) {
	for {
		select {
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return true, true
			}
			r.forwardSessionAudio(ctx, session, pcm, audioStreaming)
			handled = true
		case evt, ok := <-r.UserEventInbox:
			if !ok {
				return true, true
			}
			r.drainSessionAudio(ctx, session, audioStreaming)
			r.forwardSessionEvent(ctx, session, evt)
			handled = true
		default:
			return handled, false
		}
	}
}

func (r *ModelRunner) drainSessionAudio(ctx context.Context, session messages.Session, audioStreaming *bool) {
	for {
		select {
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return
			}
			r.forwardSessionAudio(ctx, session, pcm, audioStreaming)
		default:
			return
		}
	}
}

func (r *ModelRunner) forwardSessionAudio(ctx context.Context, session messages.Session, pcm []byte, audioStreaming *bool) {
	// Barge-in: new user audio while model is streaming an audio response.
	if *audioStreaming {
		session.Send(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeResponseCancel,
			Value: messages.NewResponseCancelValue(),
		})
		*audioStreaming = false
	}
	// Forward the user audio to the inference provider.
	session.Send(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue(pcm),
	})
}

// forwardSessionEvent preserves the legacy best-effort behavior for ordinary
// user events, but turns a rejected tool-result send into an observable stream
// error. The session lifecycle can then report the still-unresolved call ID
// instead of allowing a false clean close.
func (r *ModelRunner) forwardSessionEvent(ctx context.Context, session messages.Session, msg messages.StreamMessage) {
	outcome := messages.SendSessionWithOutcome(ctx, session, msg)
	if outcome.OK() || msg.Type != messages.StreamTypeToolCallEnd {
		return
	}
	callID := ""
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		callID = value.ToolCallID
	}
	message := fmt.Sprintf("tool result %q was not delivered: session send status %q", callID, outcome.Status)
	value := messages.NewErrorValueWithTerminal(
		message,
		"unresolved_tool_result",
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceLoop,
		messages.TerminalOutputNone,
	)
	value.Err = outcome.Err
	r.DeltaOutbox.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: value,
	})
}

func (r *ModelRunner) forwardSessionMessage(ctx context.Context, session messages.Session, msg messages.StreamMessage, audioStreaming bool, sessionClosed bool, hasOutput bool, responseCompleted bool) (bool, bool, bool, bool) {
	// Track model audio streaming state for barge-in detection.
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		// hasOutput and responseCompleted describe the current response, not
		// the lifetime of the persistent session. Reset them at each response
		// boundary so a later disconnect is classified from the latest turn.
		hasOutput = false
		responseCompleted = false
	case messages.StreamTypeAudioStart:
		audioStreaming = true
	case messages.StreamTypeAudioEnd, messages.StreamTypeMessageEnd:
		audioStreaming = false
		if msg.Type == messages.StreamTypeMessageEnd {
			responseCompleted = true
		}
	case messages.StreamTypeSessionClose:
		sessionClosed = true
		msg = normalizeSessionCloseMessage(msg)
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
	return audioStreaming, sessionClosed, hasOutput, responseCompleted
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
		// Flat tool results do not request a response themselves. Preserve the
		// established session trigger after the batch has been delivered. An
		// audio-only user turn has no text event to act as that trigger, so send
		// an explicit response request instead.
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
		// Text-only tool results retain the historical latest-user-text
		// behavior. The complete-message extension is required for rich image
		// results and should not change existing text-only session captures.
		return sessionToolResultsNotFound
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
// TOOLCALL.END per result, while the caller sends the historical latest user
// text afterward to request the next response. Image bytes cannot be
// represented by the stream-only contract, so they are intentionally not
// claimed as delivered on this path.
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
	return true
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
