package openai

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

var (
	_ messages.Session                     = (*realtimeSession)(nil)
	_ messages.SessionSendOutcomeSender    = (*realtimeSession)(nil)
	_ messages.SessionResponseRequester    = (*realtimeSession)(nil)
	_ messages.SessionResponseCapability   = (*realtimeSession)(nil)
	_ messages.SessionDropCounters         = (*realtimeSession)(nil)
	_ messages.SessionOutboundFlusher      = (*realtimeSession)(nil)
	_ sharedaudio.MediaSession             = (*realtimeSession)(nil)
	_ messages.SessionInitialConfigMarker  = (*realtimeSession)(nil)
	_ messages.SessionTerminalError        = (*realtimeSession)(nil)
	_ messages.SessionLocalPlayback        = (*realtimeSession)(nil)
	_ messages.SessionInputFormat          = (*realtimeSession)(nil)
	_ sharedaudio.ConfigurableMediaSession = (*realtimeSession)(nil)
	_ realtime.Handler                     = (*realtimeSession)(nil)
)

// realtimeSession is the OpenAI Realtime session. The shared realtime
// skeleton owns the queues, loops and RTC media; this type owns OpenAI event
// mapping and response admission.
type realtimeSession struct {
	// Surface promotes only the caller-facing session methods; base is the
	// skeleton itself, whose mutators stay private to this provider.
	realtime.Surface
	base *realtime.Session

	// clientTurnBoundaries reports that provider VAD is disabled.
	clientTurnBoundaries bool
	providerClosed       atomic.Bool

	// responseWireMu orders cancellation invalidation with response intent
	// dispatch. It is held only by callers enqueueing outbound events; the read
	// loop never waits on it while processing provider events. Lock order:
	// responseWireMu before responseMu.
	responseWireMu sync.Mutex
	// responseMu guards response, the provider-side response.create gate.
	// Realtime accepts only one active response; the read loop learns about
	// server-side response.created/response.done independently of callers
	// writing tool results. Reserve response.create before queue insertion and
	// gate tool outputs behind both pending and provider-confirmed activity.
	responseMu sync.Mutex
	response   responseState
	// responseWake signals the response intent worker.
	responseWake chan struct{}
}

// realtimeSessionSettings are the per-connection session options.
type realtimeSessionSettings struct {
	writeBackpressure, clientTurnBoundaries bool
	outputSampleRate, inputSampleRate       int
}

const maxPendingResponseIntents = 32

type responseIntent struct {
	events                []models.SessionEvent
	generation            uint64
	deferredAudioResponse bool
	settled               chan messages.SessionSendOutcome
}

func newRealtimeSession(conn transport.Conn, logger logging.Logger) *realtimeSession {
	return newConfiguredRealtimeSession(conn, logger, realtimeSessionSettings{})
}

func newConfiguredRealtimeSession(conn transport.Conn, logger logging.Logger, settings realtimeSessionSettings) *realtimeSession {
	s := &realtimeSession{
		clientTurnBoundaries: settings.clientTurnBoundaries,
		responseWake:         make(chan struct{}, 1),
	}
	s.base = realtime.NewSession(conn, logger, realtime.Config{
		LogPrefix:         "openai realtime",
		MediaName:         "OpenAI",
		WriteBackpressure: settings.writeBackpressure,
		LosslessInbound:   true,
		OutputSampleRate:  settings.outputSampleRate,
		InputSampleRate:   settings.inputSampleRate,
		WriteMediaFrame:   s.writeRTCMediaFrame,
		InterruptPlayback: s.interruptPlaybackForCancel,
		OnClose:           s.releaseResponseAdmission,
	})
	s.Surface = s.base.Surface()
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

// SupportsResponseRequests reports that RequestResponse is implemented.
func (*realtimeSession) SupportsResponseRequests() bool { return true }

func (s *realtimeSession) sendEvents(ctx context.Context, events []models.SessionEvent) messages.SessionSendOutcome {
	if ctx.Err() != nil {
		return realtime.ContextOutcome(ctx)
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
	return s.base.EnqueueEvents(ctx, events)
}

func (s *realtimeSession) admitResponseIntent(ctx context.Context, events []models.SessionEvent, reservesResponse bool) messages.SessionSendOutcome {
	if ctx.Err() != nil {
		return realtime.ContextOutcome(ctx)
	}
	if s.base.Closed() {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}

	s.responseWireMu.Lock()
	s.responseMu.Lock()
	st := &s.response
	intent := responseIntent{events: events}
	standalone := standaloneDefaultResponseIntent(intent)
	hasFunctionCallOutput := responseIntentHasFunctionCallOutput(intent)
	hasAudioCommit := responseIntentHasAudioCommit(intent)
	awaitingToolResult := standalone && st.tool == toolTurnAwaitingResult
	if awaitingToolResult && st.slot == responseSlotIdle && !hasAudioCommit {
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
	if len(st.pending) >= maxPendingResponseIntents {
		return s.unlockResponseAdmission(messages.SessionSendBufferFull)
	}
	intent = newResponseIntent(events, st.generation)
	continuesToolTurn := standalone && st.tool == toolTurnResultAdmitted
	if st.busy() {
		if standalone && st.slot == responseSlotFunctionCall && !st.tool.resultAdmitted() && !hasAudioCommit {
			// The provider chose a function-call response for this turn. A
			// standalone response.create that arrives afterward is stale; the
			// tool result's combined item-plus-create intent is the continuation
			// that must be admitted.
			return s.unlockResponseAdmission(messages.SessionSendSucceeded)
		}
		st.queue(intent, hasFunctionCallOutput)
		st.markToolResultAdmission(hasFunctionCallOutput, continuesToolTurn)
		s.unlockResponseAdmission(messages.SessionSendSucceeded)
		s.signalResponseIntentWorker()
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	return s.dispatchResponseIntentNowLocked(ctx, events, reservesResponse, hasFunctionCallOutput, continuesToolTurn)
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
	if len(s.response.pending) >= maxPendingResponseIntents {
		return s.unlockResponseAdmission(messages.SessionSendBufferFull)
	}
	commitEvents := withoutDefaultResponseCreate(events)
	responseEvents := responseCreateEvents(events)
	s.responseMu.Unlock()
	outcome := s.base.EnqueueEvents(ctx, commitEvents)
	if !outcome.OK() {
		s.responseWireMu.Unlock()
		return outcome
	}
	s.responseMu.Lock()
	if len(responseEvents) > 0 {
		s.response.pending = append(s.response.pending, responseIntent{
			events: responseEvents, generation: s.response.generation, deferredAudioResponse: true,
		})
	}
	s.unlockResponseAdmission(messages.SessionSendSucceeded)
	s.signalResponseIntentWorker()
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

// dispatchResponseIntentNowLocked writes an intent directly when no response
// is active or queued. The caller holds responseWireMu and responseMu; both
// are released on return.
func (s *realtimeSession) dispatchResponseIntentNowLocked(ctx context.Context, events []models.SessionEvent, reservesResponse, hasFunctionCallOutput, continuesToolTurn bool) messages.SessionSendOutcome {
	// A direct dispatch settles through its caller's outcome, not a
	// settlement channel.
	create, _ := firstReservingResponseCreate(events)
	s.response.beginDispatch(&responseIntent{events: events}, create, reservesResponse)
	s.responseMu.Unlock()

	outcome := s.base.EnqueueEvents(ctx, events)
	s.responseMu.Lock()
	s.response.inflight = nil
	if outcome.OK() {
		s.response.markToolResultAdmission(hasFunctionCallOutput, continuesToolTurn)
	} else if reservesResponse {
		s.response.forgetRetry()
		s.response.releaseSlot()
	}
	s.responseMu.Unlock()
	s.responseWireMu.Unlock()
	s.signalResponseIntentWorker()
	return outcome
}

func newResponseIntent(events []models.SessionEvent, generation uint64) responseIntent {
	intent := responseIntent{events: cloneSessionEvents(events), generation: generation}
	intent.settled = responseIntentSettlement(intent)
	return intent
}

func (s *realtimeSession) responseIntentLoop(ctx context.Context) {
	for {
		select {
		case <-s.Done():
			return
		case <-s.responseWake:
			s.dispatchPendingResponseIntents(ctx)
		}
	}
}

// dispatchPendingResponseIntents dispatches queued intents until none can be
// dispatched. Dispatch is not bound to the session context's cancellation: a
// popped intent is either written or settled as failed.
func (s *realtimeSession) dispatchPendingResponseIntents(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	for s.dispatchNextPendingResponseIntent(ctx) {
	}
}

// dispatchNextPendingResponseIntent dispatches or settles one queued intent
// and reports whether the dispatcher should look for another one.
func (s *realtimeSession) dispatchNextPendingResponseIntent(ctx context.Context) bool {
	s.responseWireMu.Lock()
	s.responseMu.Lock()
	st := &s.response
	if st.busyExceptQueue() || len(st.pending) == 0 {
		s.unlockResponseAdmission(messages.SessionSendSucceeded)
		return false
	}
	intent, ok := st.popPending()
	if !ok {
		s.unlockResponseAdmission(messages.SessionSendSucceeded)
		return false
	}
	if intent.generation != st.generation {
		s.unlockResponseAdmission(messages.SessionSendCancelled)
		settleResponseIntent(intent, messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled})
		return true
	}
	create, reservesResponse := firstReservingResponseCreate(intent.events)
	st.beginDispatch(&intent, create, reservesResponse)
	s.responseMu.Unlock()

	outcome := s.base.EnqueueEvents(ctx, intent.events)
	s.responseMu.Lock()
	st.inflight = nil
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
	s.responseMu.Lock()
	s.invalidatePendingResponseIntentsLocked()
	if reservesResponse {
		s.response.releaseSlot()
	}
	s.responseMu.Unlock()
	s.responseWireMu.Unlock()
	s.publishResponseIntentFailure(outcome)
}

func (s *realtimeSession) invalidatePendingResponseIntents() {
	s.responseMu.Lock()
	s.invalidatePendingResponseIntentsLocked()
	s.responseMu.Unlock()
}

// invalidatePendingResponseIntentsLocked starts a new response generation:
// queued work of the old one is settled as cancelled except tool work, and
// the function-call turn is reset. Caller holds responseMu.
func (s *realtimeSession) invalidatePendingResponseIntentsLocked() {
	st := &s.response
	st.generation++
	st.pending = st.keepToolWork(st.pending)
	st.keepContinuationRetry()
	if st.slot == responseSlotFunctionCall {
		st.slot = responseSlotActive
	}
	st.tool = toolTurnNone
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
	s.base.WriteTerminal(messages.StreamMessage{Type: messages.StreamTypeError, Value: value})
}

func (s *realtimeSession) signalResponseIntentWorker() {
	select {
	case s.responseWake <- struct{}{}:
	default:
	}
}
