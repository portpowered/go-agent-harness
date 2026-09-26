package openai

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

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
	_ messages.SessionOutboundFlusher    = (*realtimeSession)(nil)
)

type realtimeSession struct {
	conn   transport.Conn
	logger logging.Logger
	// sendQueue buffers client-to-provider events. Overflow drops are counted
	// and logged through the default observer attached below.
	sendQueue                               *messages.TypedBuffer[models.SessionEvent]
	writeBackpressure, clientTurnBoundaries bool // clientTurnBoundaries: provider VAD disabled
	outbound                                providers.OutboundWireDrain
	// recvBuf buffers translated provider-to-client events.
	recvBuf *messages.TypedBuffer[messages.StreamMessage]

	done           chan struct{}
	closeOnce      sync.Once
	errMu          sync.Mutex
	terminalErr    error
	providerClosed atomic.Bool

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
	activeResponseIntent             *responseIntent
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
	// responseDispatchFailureBarrier freezes failed dispatch cleanup while the
	// response wire lock remains held.
	responseDispatchFailureBarrier func()

	mediaMu                          sync.Mutex
	media                            *sharedaudio.SessionMedia
	mediaClaimed, mediaContinuous    bool
	mediaSampleRate, inputSampleRate int
}

const maxPendingResponseIntents = 32

type responseIntent struct {
	events                []models.SessionEvent
	generation            uint64
	deferredAudioResponse bool
	settled               chan messages.SessionSendOutcome
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
	outcome := s.sendEvents(ctx, events)
	if outcome.OK() && messages.CancelStopsPlayback(msg) {
		s.interruptPlaybackForCancel(ctx)
	}
	return outcome
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
	for _, event := range events {
		if event.Type == models.SessionEventResponseCancel {
			return s.sendResponseCancel(ctx, events)
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
	hasAudioCommit := responseIntentHasAudioCommit(intent)
	awaitingToolResult := standalone && s.suppressStandaloneResponseCreate && !s.toolResultAdmitted
	if awaitingToolResult && !s.responseActive && !hasAudioCommit {
		// A completed function-call response is followed by its tool result,
		// whose combined intent owns the continuation request. A standalone
		// response.create from the interrupted turn is stale after the
		// function-call response has ended. Combined audio intents are handled
		// below so their input_audio_buffer.commit is retained.
		return s.unlockResponseAdmission(messages.SessionSendSucceeded)
	}
	if awaitingToolResult && hasAudioCommit {
		return s.admitAudioCommitDeferringResponseLocked(ctx, events)
	}
	if len(s.pendingResponseIntents) >= maxPendingResponseIntents {
		return s.unlockResponseAdmission(messages.SessionSendBufferFull)
	}
	intent = newResponseIntent(events, s.responseGeneration)
	clearFunctionCallSuppression := standalone && s.suppressStandaloneResponseCreate && s.toolResultAdmitted
	if s.responseActive || s.responseDispatching || len(s.pendingResponseIntents) > 0 {
		if standalone && s.responseActive && s.responseHasFunctionCall && !s.toolResultAdmitted && !hasAudioCommit {
			// The provider chose a function-call response for this turn. A
			// standalone response.create that arrives afterward is stale; the
			// tool result's combined item-plus-create intent is the continuation
			// that must be admitted.
			return s.unlockResponseAdmission(messages.SessionSendSucceeded)
		}
		s.queueResponseIntentLocked(intent, hasFunctionCallOutput)
		s.markToolResultAdmissionLocked(hasFunctionCallOutput, clearFunctionCallSuppression)
		s.unlockResponseAdmission(messages.SessionSendSucceeded)
		s.signalResponseIntentWorker()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	return s.dispatchResponseIntentNowLocked(ctx, events, reservesResponse, hasFunctionCallOutput, clearFunctionCallSuppression)
}

// unlockResponseAdmission releases responseMu then responseWireMu, both held
// by the caller, and returns an outcome with status.
func (s *realtimeSession) unlockResponseAdmission(status messages.SessionSendStatus) messages.SessionSendOutcome {
	s.responseMu.Unlock()
	s.responseWireMu.Unlock()
	return messages.SessionSendOutcome{Status: status}
}

// admitAudioCommitDeferringResponseLocked delivers the audio commit while the
// provider finishes the function-call response. The audio commit is real user
// input; only its paired response.create is deferred until the
// function_call_output arrives, because dispatching that request first would
// reserve a local response slot and strand the tool result behind it. The
// caller holds responseWireMu and responseMu; both are released on return.
func (s *realtimeSession) admitAudioCommitDeferringResponseLocked(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	if len(s.pendingResponseIntents) >= maxPendingResponseIntents {
		return s.unlockResponseAdmission(messages.SessionSendBufferFull)
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
	s.unlockResponseAdmission(messages.SessionSendSucceeded)
	s.signalResponseIntentWorker()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

// queueResponseIntentLocked queues intent behind the active response. Tool
// results must precede any continuation request already queued for the same
// response: existing tool results keep arrival order, then this result is
// placed before user/audio response intents. Caller holds responseMu.
func (s *realtimeSession) queueResponseIntentLocked(intent responseIntent, hasFunctionCallOutput bool) {
	if !hasFunctionCallOutput {
		s.pendingResponseIntents = append(s.pendingResponseIntents, intent)
		return
	}
	s.dropDeferredAudioResponseIntentsLocked()
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
}

// markToolResultAdmissionLocked records an admitted tool result and clears
// function-call suppression once its continuation is admitted. Caller holds
// responseMu.
func (s *realtimeSession) markToolResultAdmissionLocked(hasFunctionCallOutput, clearFunctionCallSuppression bool) {
	if hasFunctionCallOutput {
		s.toolResultAdmitted = true
	}
	if clearFunctionCallSuppression {
		s.suppressStandaloneResponseCreate = false
		s.toolResultAdmitted = false
	}
}

// dispatchResponseIntentNowLocked writes an intent directly when no response
// is active or queued. The caller holds responseWireMu and responseMu; both
// are released on return.
func (s *realtimeSession) dispatchResponseIntentNowLocked(ctx context.Context, events []models.SessionEvent, reservesResponse, hasFunctionCallOutput, clearFunctionCallSuppression bool) messages.SessionSendOutcome {
	if reservesResponse {
		s.responseActive = true
	}
	s.responseDispatching = true
	s.responseMu.Unlock()

	if reservesResponse {
		if create, ok := firstReservingResponseCreate(events); ok {
			s.rememberResponseRequest(create)
		}
	}
	outcome := s.enqueueWireEvents(ctx, events)
	s.responseMu.Lock()
	s.responseDispatching = false
	if outcome.OK() {
		s.markToolResultAdmissionLocked(hasFunctionCallOutput, clearFunctionCallSuppression)
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

func newResponseIntent(events []models.SessionEvent, generation uint64) responseIntent {
	intent := responseIntent{events: cloneSessionEvents(events), generation: generation}
	intent.settled = responseIntentSettlement(intent)
	return intent
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
	for s.dispatchNextPendingResponseIntent() {
	}
}

// dispatchNextPendingResponseIntent dispatches or settles one queued intent
// and reports whether the dispatcher should look for another one.
func (s *realtimeSession) dispatchNextPendingResponseIntent() bool {
	s.responseWireMu.Lock()
	s.responseMu.Lock()
	if s.responseActive || s.responseDispatching || len(s.pendingResponseIntents) == 0 {
		s.unlockResponseAdmission(messages.SessionSendSucceeded)
		return false
	}
	intent, ok := s.popPendingResponseIntentLocked()
	if !ok {
		s.unlockResponseAdmission(messages.SessionSendSucceeded)
		return false
	}
	s.activeResponseIntent = &intent
	if intent.generation != s.responseGeneration {
		s.activeResponseIntent = nil
		s.unlockResponseAdmission(messages.SessionSendCancelled)
		settleResponseIntent(intent, messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled})
		return true
	}
	create, reservesResponse := firstReservingResponseCreate(intent.events)
	s.responseDispatching = true
	if reservesResponse {
		s.responseActive = true
	}
	s.responseMu.Unlock()

	if reservesResponse {
		s.rememberResponseRequest(create)
	}
	if s.responseDispatchBarrier != nil {
		s.responseDispatchBarrier()
	}
	outcome := s.enqueueWireEvents(context.Background(), intent.events)
	s.responseMu.Lock()
	s.responseDispatching = false
	s.activeResponseIntent = nil
	s.responseMu.Unlock()
	settleResponseIntent(intent, outcome)
	if !outcome.OK() {
		s.failResponseIntentDispatch(outcome, reservesResponse)
		return true
	}
	s.responseWireMu.Unlock()
	return true
}

// failResponseIntentDispatch handles a failed dispatch while the caller holds
// responseWireMu, which is released before the failure is published. A failed
// dispatch invalidates the remainder of this intent chain; continuing would
// create an ungrounded response or hide a lost tool result behind a later
// successful wire write.
func (s *realtimeSession) failResponseIntentDispatch(outcome messages.SessionSendOutcome, reservesResponse bool) {
	if s.responseDispatchFailureBarrier != nil {
		s.responseDispatchFailureBarrier()
	}
	s.invalidatePendingResponseIntents()
	if reservesResponse {
		s.releaseResponseAdmission()
	}
	s.responseWireMu.Unlock()
	s.publishResponseIntentFailure(outcome)
}

func (s *realtimeSession) popPendingResponseIntentLocked() (responseIntent, bool) {
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
			return responseIntent{}, false
		}
	}
	intent := s.pendingResponseIntents[intentIndex]
	copy(s.pendingResponseIntents[intentIndex:], s.pendingResponseIntents[intentIndex+1:])
	s.pendingResponseIntents = s.pendingResponseIntents[:len(s.pendingResponseIntents)-1]
	return intent, true
}

func (s *realtimeSession) invalidatePendingResponseIntents() {
	s.responseMu.Lock()
	s.responseGeneration++
	s.pendingResponseIntents = s.keepToolWorkLocked(s.pendingResponseIntents)
	s.keepContinuationRetryLocked()
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

func (s *realtimeSession) signalResponseIntentWorker() {
	select {
	case s.pendingResponseWake <- struct{}{}:
	default:
	}
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
		closeErr = errors.Join(s.currentRTCMedia().Close(), s.conn.Close())
	})
	return closeErr
}
