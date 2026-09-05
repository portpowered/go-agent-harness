package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	_ messages.Session                   = (*realtimeSession)(nil)
	_ messages.SessionSendOutcomeSender  = (*realtimeSession)(nil)
	_ messages.SessionResponseRequester  = (*realtimeSession)(nil)
	_ messages.SessionResponseCapability = (*realtimeSession)(nil)
	_ messages.SessionDropCounters       = (*realtimeSession)(nil)
)

type realtimeSession struct {
	conn   transport.Conn
	logger logging.Logger
	// sendQueue buffers outbound wire events (client-to-provider, the
	// session's input path). Overflow drops are counted by the buffer itself
	// and logged through the default drop observer attached below.
	sendQueue *messages.TypedBuffer[models.SessionEvent]
	// recvBuf buffers translated inbound events (provider-to-client, the
	// session's output path).
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done        chan struct{}
	closeOnce   sync.Once
	errMu       sync.Mutex
	terminalErr error

	// responseAdmission is the provider-side response.create gate. Realtime
	// accepts only one active response; the read loop learns about server-side
	// response.created/response.done independently of callers writing tool
	// results. Reserve response.create before queue insertion and gate tool
	// outputs behind both pending and provider-confirmed activity.
	responseMu                       sync.Mutex
	responseActive                   bool
	responseID                       string
	responseHasFunctionCall          bool
	suppressStandaloneResponseCreate bool
	toolResultAdmitted               bool
	responseDone                     chan struct{}
	responseRetry                    *models.SessionEvent
	responseSent                     bool
	responseRetryPending             bool
	responseGeneration               uint64
	responseDispatching              bool
	pendingResponseIntents           []responseIntent
	pendingResponseWake              chan struct{}
	// responseWireMu orders cancellation invalidation with response intent
	// dispatch. It is held only by callers enqueueing outbound events; the read
	// loop never waits on it while processing provider events.
	responseWireMu sync.Mutex
	// responseDispatchBarrier is an internal synchronization seam used by the
	// admission regression tests. It runs after an intent is popped but before
	// its events are placed on sendQueue, while responseWireMu is held. Keeping
	// the hook here makes pop/enqueue/cancel ordering testable without delaying
	// the read loop or exposing a production control surface.
	responseDispatchBarrier func()
	// responseDispatchFailureBarrier freezes a failed dispatch before its
	// generation is invalidated, proving that responseWireMu remains held
	// across failure cleanup.
	responseDispatchFailureBarrier func()

	mediaMu         sync.Mutex
	media           *sharedaudio.SessionMedia
	mediaClaimed    bool
	mediaSampleRate int
}

const maxPendingResponseIntents = 32

type responseIntent struct {
	events                []models.SessionEvent
	generation            uint64
	deferredAudioResponse bool
}

var _ messages.SessionSendOutcomeSender = (*realtimeSession)(nil)

func newRealtimeSession(conn transport.Conn, logger logging.Logger) *realtimeSession {
	s := &realtimeSession{
		conn:                conn,
		logger:              logger,
		sendQueue:           messages.NewTypedBuffer[models.SessionEvent](64),
		recvBuf:             messages.NewTypedBuffer[messages.StreamMessage](64),
		done:                make(chan struct{}),
		responseDone:        make(chan struct{}),
		pendingResponseWake: make(chan struct{}, 1),
	}
	providers.AttachSessionDropLoggers(logger, s.sendQueue, s.recvBuf)
	return s
}

func (s *realtimeSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome admits a StreamMessage to the session's outbound wire queue
// or bounded response-intent queue and reports that local admission outcome.
// A response intent accepted while another response is active is dispatched by
// the independent response worker; a later dispatch failure is surfaced as a
// terminal stream error because it occurs after this method returns.
func (s *realtimeSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	events, ok := realtimeOutboundEvents(msg)
	if !ok {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.sendEvents(ctx, events)
}

// RequestResponse starts a response without adding another user turn. This is
// needed when a tool result follows an audio-only input, whose history has no
// text event that can request the continuation.
func (s *realtimeSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (*realtimeSession) SupportsResponseRequests() bool { return true }

func (s *realtimeSession) sendEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	default:
	}
	needsAdmission := false
	reservesResponse := false
	for _, event := range events {
		if realtimeEventNeedsResponseAdmission(event) {
			needsAdmission = true
		}
		if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
			reservesResponse = true
		}
	}
	if needsAdmission {
		return s.admitResponseIntent(ctx, events, reservesResponse)
	}
	// RESPONSE.CANCEL is deliberately outside the response intent queue. It
	// must reach the provider even while a default response is active and
	// invalidates queued work from the cancelled generation.
	for _, event := range events {
		if event.Type == models.SessionEventResponseCancel {
			s.responseWireMu.Lock()
			s.invalidatePendingResponseIntents()
			outcome := s.enqueueWireEvents(ctx, events)
			s.responseWireMu.Unlock()
			return outcome
		}
	}
	return s.enqueueWireEvents(ctx, events)
}

func (s *realtimeSession) admitResponseIntent(ctx context.Context, events []models.SessionEvent, reservesResponse bool) messages.SessionSendOutcome {
	select {
	case <-ctx.Done():
		return sessionSendContextOutcome(ctx)
	case <-s.done:
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	default:
	}

	s.responseWireMu.Lock()
	s.responseMu.Lock()
	intent := responseIntent{events: events}
	standalone := standaloneDefaultResponseIntent(intent)
	hasFunctionCallOutput := responseIntentHasFunctionCallOutput(intent)
	if standalone && s.suppressStandaloneResponseCreate && !s.toolResultAdmitted && !s.responseActive && !responseIntentHasAudioCommit(intent) {
		// A completed function-call response is followed by its tool result,
		// whose combined intent owns the continuation request. A standalone
		// response.create from the interrupted turn is stale after the
		// function-call response has ended. Combined audio intents are handled
		// below so their input_audio_buffer.commit is retained.
		s.responseMu.Unlock()
		s.responseWireMu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if standalone && s.suppressStandaloneResponseCreate && !s.toolResultAdmitted && responseIntentHasAudioCommit(intent) {
		// The audio commit is real user input and can be delivered while the
		// provider finishes the function-call response. Defer only its paired
		// response.create until the function_call_output arrives; dispatching
		// that request first would reserve a local response slot and strand the
		// tool result behind it.
		if len(s.pendingResponseIntents) >= maxPendingResponseIntents {
			s.responseMu.Unlock()
			s.responseWireMu.Unlock()
			return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
		}
		commitEvents := withoutDefaultResponseCreate(events)
		responseEvents := responseCreateEvents(events)
		s.responseMu.Unlock()
		outcome := s.enqueueWireEvents(ctx, commitEvents)
		if !outcome.OK() {
			s.responseWireMu.Unlock()
			return outcome
		}
		s.responseMu.Lock()
		if len(responseEvents) > 0 {
			s.pendingResponseIntents = append(s.pendingResponseIntents, responseIntent{
				events: responseEvents, generation: s.responseGeneration, deferredAudioResponse: true,
			})
		}
		s.responseMu.Unlock()
		s.responseWireMu.Unlock()
		s.signalResponseIntentWorker()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if len(s.pendingResponseIntents) >= maxPendingResponseIntents {
		s.responseMu.Unlock()
		s.responseWireMu.Unlock()
		return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
	}
	intent = responseIntent{events: cloneSessionEvents(events), generation: s.responseGeneration}
	clearFunctionCallSuppression := standalone && s.suppressStandaloneResponseCreate && s.toolResultAdmitted
	if s.responseActive || s.responseDispatching || len(s.pendingResponseIntents) > 0 {
		if standalone && s.responseActive && s.responseHasFunctionCall && !s.toolResultAdmitted && !responseIntentHasAudioCommit(intent) {
			// The provider chose a function-call response for this turn. A
			// standalone response.create that arrives afterward is stale; the
			// tool result's combined item-plus-create intent is the continuation
			// that must be admitted.
			s.responseMu.Unlock()
			s.responseWireMu.Unlock()
			return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
		}
		if hasFunctionCallOutput {
			s.dropDeferredAudioResponseIntentsLocked()
			// Tool results must precede any continuation request already queued
			// for the same response. Keep existing tool results in arrival order,
			// then place this result before user/audio response intents.
			insertAt := len(s.pendingResponseIntents)
			for index, pending := range s.pendingResponseIntents {
				if !responseIntentHasFunctionCallOutput(pending) {
					insertAt = index
					break
				}
			}
			s.pendingResponseIntents = append(s.pendingResponseIntents, responseIntent{})
			copy(s.pendingResponseIntents[insertAt+1:], s.pendingResponseIntents[insertAt:])
			s.pendingResponseIntents[insertAt] = intent
		} else {
			s.pendingResponseIntents = append(s.pendingResponseIntents, intent)
		}
		if hasFunctionCallOutput {
			s.toolResultAdmitted = true
		}
		if clearFunctionCallSuppression {
			s.suppressStandaloneResponseCreate = false
			s.toolResultAdmitted = false
		}
		s.responseMu.Unlock()
		s.responseWireMu.Unlock()
		s.signalResponseIntentWorker()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if reservesResponse {
		s.responseActive = true
	}
	s.responseDispatching = true
	s.responseMu.Unlock()

	if reservesResponse {
		for _, event := range events {
			if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
				s.rememberResponseRequest(event)
				break
			}
		}
	}
	outcome := s.enqueueWireEvents(ctx, events)
	s.responseMu.Lock()
	s.responseDispatching = false
	if outcome.OK() {
		if hasFunctionCallOutput {
			s.toolResultAdmitted = true
		}
		if clearFunctionCallSuppression {
			s.suppressStandaloneResponseCreate = false
			s.toolResultAdmitted = false
		}
	}
	s.responseMu.Unlock()
	s.responseWireMu.Unlock()
	if !outcome.OK() && reservesResponse {
		s.forgetResponseRequest()
		s.releaseResponseAdmission()
	}
	s.signalResponseIntentWorker()
	return outcome
}

func cloneSessionEvents(events []models.SessionEvent) []models.SessionEvent {
	cloned := make([]models.SessionEvent, len(events))
	for index, event := range events {
		cloned[index] = event
		if event.Data != nil {
			cloned[index].Data = append(json.RawMessage(nil), event.Data...)
		}
	}
	return cloned
}

func (s *realtimeSession) signalResponseIntentWorker() {
	select {
	case s.pendingResponseWake <- struct{}{}:
	default:
	}
}

func (s *realtimeSession) responseIntentLoop() {
	for {
		select {
		case <-s.done:
			return
		case <-s.pendingResponseWake:
			s.dispatchPendingResponseIntents()
		}
	}
}

func (s *realtimeSession) dispatchPendingResponseIntents() {
	for {
		s.responseWireMu.Lock()
		s.responseMu.Lock()
		if s.responseActive || s.responseDispatching || len(s.pendingResponseIntents) == 0 {
			s.responseMu.Unlock()
			s.responseWireMu.Unlock()
			return
		}
		intentIndex := 0
		if s.suppressStandaloneResponseCreate && !s.toolResultAdmitted {
			intentIndex = -1
			for index, pending := range s.pendingResponseIntents {
				if responseIntentHasFunctionCallOutput(pending) {
					intentIndex = index
					break
				}
			}
			if intentIndex < 0 {
				s.responseMu.Unlock()
				s.responseWireMu.Unlock()
				return
			}
		}
		intent := s.pendingResponseIntents[intentIndex]
		copy(s.pendingResponseIntents[intentIndex:], s.pendingResponseIntents[intentIndex+1:])
		s.pendingResponseIntents = s.pendingResponseIntents[:len(s.pendingResponseIntents)-1]
		if intent.generation != s.responseGeneration {
			s.responseMu.Unlock()
			s.responseWireMu.Unlock()
			continue
		}
		reservesResponse := false
		for _, event := range intent.events {
			if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
				reservesResponse = true
				break
			}
		}
		s.responseDispatching = true
		if reservesResponse {
			s.responseActive = true
		}
		s.responseMu.Unlock()

		if reservesResponse {
			for _, event := range intent.events {
				if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
					s.rememberResponseRequest(event)
					break
				}
			}
		}
		if s.responseDispatchBarrier != nil {
			s.responseDispatchBarrier()
		}
		outcome := s.enqueueWireEvents(context.Background(), intent.events)
		s.responseMu.Lock()
		s.responseDispatching = false
		s.responseMu.Unlock()
		if !outcome.OK() {
			// A failed dispatch invalidates the remainder of this intent chain;
			// continuing would create an ungrounded response or hide a lost tool
			// result behind a later successful wire write.
			if s.responseDispatchFailureBarrier != nil {
				s.responseDispatchFailureBarrier()
			}
			s.invalidatePendingResponseIntents()
			if reservesResponse {
				s.releaseResponseAdmission()
			}
			s.responseWireMu.Unlock()
			s.publishResponseIntentFailure(outcome)
			continue
		}
		s.responseWireMu.Unlock()
	}
}

func (s *realtimeSession) invalidatePendingResponseIntents() {
	s.responseMu.Lock()
	s.responseGeneration++
	s.pendingResponseIntents = nil
	s.responseRetry = nil
	s.responseSent = false
	s.responseRetryPending = false
	s.responseHasFunctionCall = false
	s.suppressStandaloneResponseCreate = false
	s.toolResultAdmitted = false
	s.responseMu.Unlock()
}

func (s *realtimeSession) publishResponseIntentFailure(outcome messages.SessionSendOutcome) {
	message := fmt.Sprintf("queued response intent was not delivered: status %q", outcome.Status)
	value := messages.NewErrorValueWithTerminal(
		message,
		"response_intent_dispatch_failed",
		messages.TerminalReasonTerminalFailure,
		messages.TerminalProvenanceGateway,
		messages.TerminalOutputNone,
	)
	value.Err = outcome.Err
	s.recvBuf.WriteTerminal(messages.StreamMessage{Type: messages.StreamTypeError, Value: value})
}

func (s *realtimeSession) enqueueWireEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	for _, event := range events {
		// A terminated session reports closed regardless of remaining
		// outbound buffer capacity.
		select {
		case <-s.done:
			return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
		default:
		}
		outcome := s.sendQueue.WriteContext(ctx, event)
		switch outcome.Status {
		case messages.BufferWriteSucceeded:
		case messages.BufferWriteBufferFull:
			return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull}
		default:
			return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *realtimeSession) rememberResponseRequest(event models.SessionEvent) {
	s.responseMu.Lock()
	copyEvent := event
	s.responseRetry = &copyEvent
	s.responseSent = false
	s.responseRetryPending = false
	s.responseMu.Unlock()
}

func (s *realtimeSession) forgetResponseRequest() {
	s.responseMu.Lock()
	s.responseRetry = nil
	s.responseSent = false
	s.responseRetryPending = false
	s.responseMu.Unlock()
}

func (s *realtimeSession) markResponseRequestSent(event models.SessionEvent) {
	if event.Type != models.SessionEventResponseCreate || realtimeResponseCreateIsOutOfBand(event) {
		return
	}
	s.responseMu.Lock()
	if s.responseRetry != nil {
		s.responseSent = true
	}
	s.responseMu.Unlock()
}

func realtimeEventNeedsResponseAdmission(event models.SessionEvent) bool {
	if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
		return true
	}
	if event.Type != conversationItemCreateEvent {
		return false
	}
	// A late function_call_output belongs to the response that produced the
	// call. Hold it with its continuation while an unrelated response is
	// active, preserving the provider's required item-then-create boundary.
	var payload struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Item.Type == "function_call_output"
}

func realtimeResponseCreateIsOutOfBand(event models.SessionEvent) bool {
	if event.Type != models.SessionEventResponseCreate || len(event.Data) == 0 {
		return false
	}
	var payload struct {
		Response struct {
			Conversation string `json:"conversation"`
		} `json:"response"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Response.Conversation == "none"
}

func (s *realtimeSession) releaseResponseAdmission() {
	s.responseMu.Lock()
	if s.responseActive {
		s.responseActive = false
		s.responseID = ""
		s.responseHasFunctionCall = false
		s.suppressStandaloneResponseCreate = false
		s.toolResultAdmitted = false
		close(s.responseDone)
		s.responseDone = make(chan struct{})
	}
	s.responseMu.Unlock()
}

func (s *realtimeSession) observeResponseCreated(event models.SessionEvent) {
	if realtimeResponseCreatedIsOutOfBand(event) {
		return
	}
	responseID := firstStringField(event.Data, "response_id", "response.id")
	s.responseMu.Lock()
	if !s.responseActive {
		s.suppressStandaloneResponseCreate = false
	}
	// A provider may start a replacement response after barge-in while a
	// standalone response.create for the interrupted turn is still queued.
	// That request is stale: dispatching it after this response completes would
	// reserve the provider's single response slot ahead of the replacement's
	// function_call_output, potentially blocking the real continuation forever.
	// Preserve combined tool-result intents; only retire standalone default
	// response requests that were waiting for the superseded response.
	if s.responseActive && s.responseID != "" && responseID != "" && s.responseID != responseID {
		s.dropStandaloneResponseIntentsLocked()
	}
	s.responseActive = true
	s.responseID = responseID
	s.responseHasFunctionCall = false
	s.responseMu.Unlock()
}

func (s *realtimeSession) dropStandaloneResponseIntentsLocked() {
	if len(s.pendingResponseIntents) == 0 {
		return
	}
	kept := s.pendingResponseIntents[:0]
	for _, intent := range s.pendingResponseIntents {
		if !standaloneDefaultResponseIntent(intent) || responseIntentHasAudioCommit(intent) {
			kept = append(kept, intent)
			continue
		}
		// Retire only the stale response request. The remaining events belong
		// to the user turn and must retain their original queue position.
		if events := withoutDefaultResponseCreate(intent.events); len(events) > 0 {
			intent.events = events
			kept = append(kept, intent)
		}
	}
	s.pendingResponseIntents = kept
}

func (s *realtimeSession) dropDeferredAudioResponseIntentsLocked() {
	if len(s.pendingResponseIntents) == 0 {
		return
	}
	kept := s.pendingResponseIntents[:0]
	for _, intent := range s.pendingResponseIntents {
		if intent.deferredAudioResponse {
			continue
		}
		kept = append(kept, intent)
	}
	s.pendingResponseIntents = kept
}

func standaloneDefaultResponseIntent(intent responseIntent) bool {
	hasResponseCreate := false
	for _, event := range intent.events {
		switch event.Type {
		case models.SessionEventResponseCreate:
			if realtimeResponseCreateIsOutOfBand(event) {
				return false
			}
			hasResponseCreate = true
		case conversationItemCreateEvent:
			if responseEventIsFunctionCallOutput(event) {
				return false
			}
			// A user message plus response.create is a fresh turn. It may
			// legitimately be queued while a function-call response is active,
			// so it must never be classified as a stale standalone request.
			return false
		}
	}
	return hasResponseCreate
}

func withoutDefaultResponseCreate(events []models.SessionEvent) []models.SessionEvent {
	kept := make([]models.SessionEvent, 0, len(events))
	for _, event := range events {
		if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
			continue
		}
		kept = append(kept, event)
	}
	return kept
}

func responseCreateEvents(events []models.SessionEvent) []models.SessionEvent {
	kept := make([]models.SessionEvent, 0, 1)
	for _, event := range events {
		if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
			kept = append(kept, event)
		}
	}
	return kept
}

func responseIntentHasFunctionCallOutput(intent responseIntent) bool {
	for _, event := range intent.events {
		if responseEventIsFunctionCallOutput(event) {
			return true
		}
	}
	return false
}

func responseIntentHasAudioCommit(intent responseIntent) bool {
	for _, event := range intent.events {
		if event.Type == models.SessionEventInputAudioBufferCommit {
			return true
		}
	}
	return false
}

func responseEventIsFunctionCallOutput(event models.SessionEvent) bool {
	if event.Type != conversationItemCreateEvent {
		return false
	}
	var payload struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Item.Type == "function_call_output"
}

func realtimeResponseCreatedIsOutOfBand(event models.SessionEvent) bool {
	if event.Type != models.SessionEventResponseCreated || len(event.Data) == 0 {
		return false
	}
	var payload struct {
		Response struct {
			Conversation string `json:"conversation"`
		} `json:"response"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Response.Conversation == "none"
}

func realtimeResponseDoneIsOutOfBand(event models.SessionEvent) bool {
	if event.Type != models.SessionEventResponseDone || len(event.Data) == 0 {
		return false
	}
	var payload struct {
		Response struct {
			Conversation string `json:"conversation"`
		} `json:"response"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Response.Conversation == "none"
}

func (s *realtimeSession) observeResponseDone(event models.SessionEvent) {
	if realtimeResponseDoneIsOutOfBand(event) {
		return
	}
	doneID := firstStringField(event.Data, "response_id", "response.id")
	s.responseMu.Lock()
	if s.responseActive && ((s.responseID != "" && doneID == "") || (s.responseID == "" && doneID != "") || (s.responseID != "" && doneID != "" && s.responseID != doneID)) {
		s.responseMu.Unlock()
		return
	}
	if !s.responseActive {
		s.responseMu.Unlock()
		return
	}
	if s.responseRetryPending && s.responseRetry != nil {
		retry := *s.responseRetry
		pending := make([]responseIntent, 0, len(s.pendingResponseIntents)+1)
		pending = append(pending, responseIntent{events: []models.SessionEvent{retry}, generation: s.responseGeneration})
		pending = append(pending, s.pendingResponseIntents...)
		s.pendingResponseIntents = pending
		s.responseActive = false
		s.responseRetry = nil
		s.responseSent = false
		s.responseRetryPending = false
		s.responseID = ""
		s.responseHasFunctionCall = false
		close(s.responseDone)
		s.responseDone = make(chan struct{})
		s.responseMu.Unlock()
		s.signalResponseIntentWorker()
		return
	}
	s.responseActive = false
	s.responseID = ""
	s.responseHasFunctionCall = false
	s.responseRetry = nil
	s.responseSent = false
	close(s.responseDone)
	s.responseDone = make(chan struct{})
	s.responseMu.Unlock()
	s.signalResponseIntentWorker()
}

func (s *realtimeSession) observeResponseCreateActiveError(event models.SessionEvent) {
	if event.Type != models.SessionEventError ||
		firstStringField(event.Data, "error.type") != realtimeInvalidRequestErrorType ||
		firstStringField(event.Data, "error.code") != realtimeResponseCreateActiveCode {
		return
	}
	s.responseMu.Lock()
	if s.responseActive && s.responseRetry != nil && s.responseSent {
		s.responseRetryPending = true
	}
	s.responseMu.Unlock()
}

func (s *realtimeSession) observeResponseCancelRejection(event models.SessionEvent) {
	if event.Type != models.SessionEventError ||
		firstStringField(event.Data, "error.type") != realtimeInvalidRequestErrorType ||
		firstStringField(event.Data, "error.code") != realtimeResponseCancelNotActiveCode {
		return
	}
	// The provider says there is no active response. Clear a stale local
	// reservation so a queued continuation cannot remain blocked forever.
	s.releaseResponseAdmission()
}

func sessionSendContextOutcome(ctx context.Context) messages.SessionSendOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *realtimeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	s.releaseUnclaimedRTCMedia()
	return s.recvBuf
}

// InputDrops reports cumulative drops on the client-to-provider send queue.
func (s *realtimeSession) InputDrops() int64 { return s.sendQueue.Drops() }

// OutputDrops reports cumulative drops on the provider-to-client receive buffer.
func (s *realtimeSession) OutputDrops() int64 { return s.recvBuf.Drops() }

func (s *realtimeSession) Done() <-chan struct{} {
	return s.done
}

// TerminalError returns the unexpected provider-side transport or protocol
// error that terminated the session, if one was observed. A clean caller-side
// Close and context cancellation do not set this value.
func (s *realtimeSession) TerminalError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.terminalErr
}

func (s *realtimeSession) setTerminalError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	if s.terminalErr == nil {
		s.terminalErr = err
	}
	s.errMu.Unlock()
}

func (s *realtimeSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		s.releaseResponseAdmission()
		if media := s.currentRTCMedia(); media != nil {
			_ = media.Close()
		}
		closeErr = s.conn.Close()
	})
	return closeErr
}
