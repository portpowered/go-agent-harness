package participants

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-loop/pkg/messages"
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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-session.Done():
			for {
				msg, ok := session.Receive().Read()
				if !ok {
					break
				}
				audioStreaming, sessionClosed = r.forwardSessionMessage(ctx, session, msg, audioStreaming, sessionClosed)
			}
			if !sessionClosed {
				r.DeltaOutbox.Write(ctx, messages.StreamMessage{
					Type: messages.StreamTypeSessionClose,
					Value: messages.NewSessionCloseValueWithTerminal(
						"",
						"provider_closed",
						string(messages.TerminalReasonProviderClose),
						messages.TerminalReasonProviderClose,
						messages.TerminalProvenanceSession,
						messages.TerminalOutputNotApplicable,
					),
				})
			}
			return nil
		case pcm, ok := <-r.UserAudioInbox:
			if !ok {
				return nil
			}
			// Barge-in: new user audio while model is streaming an audio response.
			if audioStreaming {
				session.Send(ctx, messages.StreamMessage{
					Type:  messages.StreamTypeResponseCancel,
					Value: messages.NewResponseCancelValue(),
				})
				audioStreaming = false
			}
			// Forward the user audio to the inference provider.
			session.Send(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeAudioDelta,
				Value: messages.NewAudioDeltaValue(pcm),
			})
		case req, ok := <-r.Inbox.Chan():
			if !ok {
				return nil
			}
			r.sendLatestUserText(ctx, session, req)
		case msg, ok := <-session.Receive().Chan():
			if !ok {
				return nil
			}
			audioStreaming, sessionClosed = r.forwardSessionMessage(ctx, session, msg, audioStreaming, sessionClosed)
		}
	}
}

func (r *ModelRunner) forwardSessionMessage(ctx context.Context, session messages.Session, msg messages.StreamMessage, audioStreaming bool, sessionClosed bool) (bool, bool) {
	// Track model audio streaming state for barge-in detection.
	switch msg.Type {
	case messages.StreamTypeAudioStart:
		audioStreaming = true
	case messages.StreamTypeAudioEnd, messages.StreamTypeMessageEnd:
		audioStreaming = false
	case messages.StreamTypeSessionClose:
		sessionClosed = true
		msg = normalizeSessionCloseMessage(msg)
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
	return audioStreaming, sessionClosed
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
		value.Classification = string(value.TerminalReason)
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
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != messages.RoleUser {
			continue
		}
		text := msg.TextContent()
		if text == "" {
			return
		}
		session.Send(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue(text),
		})
		return
	}
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
		Type: messages.StreamTypeMessageEnd,
		Role: messages.RoleAssistant,
		Value: messages.NewMessageEndValueWithTerminal(
			result.TokenUsage,
			messages.TerminalReasonLoopSynthesizedCompletion,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputComplete,
		),
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
