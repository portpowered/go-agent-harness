package live

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const (
	defaultProviderLivenessTimeout = 10 * time.Second
	silentProviderEmptyResponse    = "silent_provider_empty_response"
	silentProviderTimeout          = "silent_provider_timeout"
)

// providerLivenessError preserves the stable sentinel while keeping the
// participant's bounded terminal taxonomy available to room and CLI owners.
type providerLivenessError struct {
	failure session.LiveLivenessFailure
}

func (e *providerLivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	classification := strings.TrimSpace(e.failure.Classification)
	if classification == "" {
		classification = silentProviderEmptyResponse
	}
	return fmt.Sprintf("%s: provider response produced no observable output", classification)
}

func (e *providerLivenessError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.failure.Classification == silentProviderTimeout {
		return session.ErrLiveSilentProviderTimeout
	}
	return session.ErrLiveSilentProviderEmptyResponse
}

func (h *handle) providerLivenessEnabled() bool {
	if h == nil {
		return false
	}
	return h.request.ProviderLiveness.Enabled || h.request.ProviderLiveness.Timeout > 0
}

func (h *handle) providerLivenessTimeout() time.Duration {
	if h == nil || h.request.ProviderLiveness.Timeout <= 0 {
		return defaultProviderLivenessTimeout
	}
	return h.request.ProviderLiveness.Timeout
}

func (h *handle) watchProviderLiveness(ctx context.Context) {
	defer h.runWG.Done()
	for {
		h.livenessMu.Lock()
		if h.livenessStopped || h.livenessFailure != nil {
			h.livenessMu.Unlock()
			return
		}
		generation := h.livenessGeneration
		var timerCh <-chan time.Time
		if h.livenessArmed && h.livenessTimer != nil {
			timerCh = h.livenessTimer.C()
		}
		wake := h.livenessWake
		h.livenessMu.Unlock()

		select {
		case <-timerCh:
			h.expireProviderLiveness(generation) //nolint:contextcheck // Publication uses the handle's invocation evidence context so terminal observations survive run cancellation.
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

func (h *handle) armProviderLiveness() {
	h.setProviderLiveness(false)
}

func (h *handle) resetProviderLiveness() {
	h.setProviderLiveness(true)
}

func (h *handle) setProviderLiveness(onlyIfArmed bool) {
	if h == nil || !h.providerLivenessEnabled() || h.scheduler == nil {
		return
	}
	h.livenessMu.Lock()
	if h.livenessStopped || h.livenessFailure != nil || (onlyIfArmed && !h.livenessArmed) {
		h.livenessMu.Unlock()
		return
	}
	h.livenessMu.Unlock()

	timer := h.scheduler.NewTimer(h.providerLivenessTimeout())
	if timer == nil {
		h.Cancel(fmt.Errorf("create provider liveness timer: %w", session.ErrLiveSchedulerUnavailable))
		return
	}
	h.livenessMu.Lock()
	if h.livenessStopped || h.livenessFailure != nil || (onlyIfArmed && !h.livenessArmed) {
		h.livenessMu.Unlock()
		timer.Stop()
		return
	}
	oldTimer := h.livenessTimer
	h.livenessTimer = timer
	h.livenessArmed = true
	h.livenessGeneration++
	wake := h.livenessWake
	h.livenessMu.Unlock()
	if oldTimer != nil {
		oldTimer.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (h *handle) disarmProviderLiveness() {
	if h == nil {
		return
	}
	h.livenessMu.Lock()
	if !h.livenessArmed && h.livenessTimer == nil {
		h.livenessMu.Unlock()
		return
	}
	h.livenessArmed = false
	h.livenessGeneration++
	timer := h.livenessTimer
	h.livenessTimer = nil
	wake := h.livenessWake
	h.livenessMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (h *handle) stopProviderLiveness() {
	if h == nil {
		return
	}
	h.livenessMu.Lock()
	if h.livenessStopped {
		h.livenessMu.Unlock()
		return
	}
	h.livenessStopped = true
	h.livenessArmed = false
	h.livenessGeneration++
	timer := h.livenessTimer
	h.livenessTimer = nil
	wake := h.livenessWake
	h.livenessMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (h *handle) expireProviderLiveness(generation uint64) {
	if h == nil {
		return
	}
	h.livenessMu.Lock()
	if h.livenessStopped || h.livenessFailure != nil || !h.livenessArmed || h.livenessGeneration != generation {
		h.livenessMu.Unlock()
		return
	}
	h.livenessMu.Unlock()
	failure := session.LiveLivenessFailure{
		Classification:     silentProviderTimeout,
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
	h.latchProviderLiveness(failure)
}

func (h *handle) observeProviderLiveness(ctx context.Context, msg messages.StreamMessage) {
	if h == nil || !h.providerLivenessEnabled() || msg.Role == messages.RoleTool || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return
	}
	if msg.Type == messages.StreamTypeMessageStart {
		h.livenessMu.Lock()
		h.responseOutputSeen = false
		h.responseToolObligation = false
		h.livenessMu.Unlock()
		h.armProviderLiveness()
		return
	}
	if isProviderOutputMessage(msg) {
		h.livenessMu.Lock()
		h.responseOutputSeen = true
		h.livenessMu.Unlock()
	}
	if msg.Type == messages.StreamTypeToolCallStart || msg.Type == messages.StreamTypeToolCallDelta || msg.Type == messages.StreamTypeToolCallEnd {
		h.livenessMu.Lock()
		h.responseToolObligation = true
		h.livenessMu.Unlock()
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		h.observeProviderMessageEnd(ctx, msg)
		return
	}
	if msg.Type == messages.StreamTypeSessionClose || msg.Type == messages.StreamTypeError {
		h.disarmProviderLiveness()
		return
	}
	h.resetProviderLiveness()
}

// observeProviderDispatch arms the participant watchdog when the loop admits
// an operation that asks the provider to produce a response. The provider may
// emit MESSAGE.START asynchronously, so arming at dispatch closes the gap
// between a successful input admission and the first provider observation.
func (h *handle) observeProviderDispatch(msg messages.StreamMessage) {
	if h == nil {
		return
	}
	h.recordMessage(session.LiveRecord{Direction: session.LiveRecordClient, Timestamp: h.now(), Message: msg})
	if msg.Type != messages.StreamTypeResponseCreate && msg.Type != messages.StreamTypeMessageEnd {
		return
	}
	value, hasCreate := msg.Value.(*messages.ResponseCreateValue)
	acknowledgement := hasCreate && value != nil && value.IsToolAcknowledgement()
	// Admission establishes pending work even before the provider's response
	// starts. Keep that obligation separate from observed streaming progress.
	if !acknowledgement && msg.Role != messages.RoleTool {
		h.mu.Lock()
		h.responseStarted, h.responsePending = true, true
		if msg.Type == messages.StreamTypeResponseCreate {
			h.responseActive = true
		}
		h.mu.Unlock()
	}
	if h.providerLivenessEnabled() && (msg.Type == messages.StreamTypeMessageEnd || !acknowledgement) {
		h.armProviderLiveness()
	}
}

func (h *handle) observeProviderMessageEnd(_ context.Context, msg messages.StreamMessage) {
	value, ok := msg.Value.(*messages.MessageEndValue)
	if !ok {
		value = nil
	}
	h.livenessMu.Lock()
	outputSeen := h.responseOutputSeen
	toolObligation := h.responseToolObligation
	h.responseOutputSeen = false
	h.responseToolObligation = false
	h.livenessMu.Unlock()
	if isEmptyProviderResponse(msg, value, outputSeen, toolObligation) {
		failure := session.LiveLivenessFailure{
			Classification:     silentProviderEmptyResponse,
			ResponseID:         strings.TrimSpace(msg.ResponseID),
			TerminalReason:     messages.TerminalReasonTerminalFailure,
			TerminalProvenance: messages.TerminalProvenanceSession,
			OutputState:        messages.TerminalOutputNone,
		}
		if value != nil {
			failure.Usage = value.Usage
		}
		h.latchProviderLiveness(failure) //nolint:contextcheck // Publication uses the handle's retained invocation evidence context, not this observation callback's cancellation.
		return
	}
	h.disarmProviderLiveness()
}

func isProviderOutputMessage(msg messages.StreamMessage) bool {
	if msg.Role == messages.RoleUser || msg.Role == messages.RoleTool {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeTextDelta, messages.StreamTypeAudioDelta, messages.StreamTypeImageDelta,
		messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta, messages.StreamTypeEmbeddingDelta,
		messages.StreamTypeReasoningDelta, messages.StreamTypeTranscriptDelta,
		messages.StreamTypeTextEnd, messages.StreamTypeAudioEnd, messages.StreamTypeImageEnd,
		messages.StreamTypeVideoEnd, messages.StreamTypeFileEnd, messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningEnd, messages.StreamTypeTranscriptEnd:
		return true
	case messages.StreamTypeMessageStart, messages.StreamTypeMessageEnd, messages.StreamTypeTextStart,
		messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioStart, messages.StreamTypeImageStart, messages.StreamTypeVideoStart,
		messages.StreamTypeFileStart, messages.StreamTypeEmbeddingStart, messages.StreamTypeReasoningStart,
		messages.StreamTypeVADSpeechStarted, messages.StreamTypeVADSpeechStopped, messages.StreamTypeTranscriptStart,
		messages.StreamTypeInputItemAdded, messages.StreamTypePong, messages.StreamTypeSessionOpen,
		messages.StreamTypeSessionClose, messages.StreamTypeSessionCreated, messages.StreamTypeSessionUpdated,
		messages.StreamTypeSessionUpdate, messages.StreamTypeResponseCancel, messages.StreamTypeResponseCreate,
		messages.StreamTypeRefusal, messages.StreamTypeLoopEnd, messages.StreamTypeUsageInfo,
		messages.StreamTypeError, messages.StreamTypeSystemFullMessage:
		return false
	default:
		return false
	}
}

func isEmptyProviderResponse(msg messages.StreamMessage, value *messages.MessageEndValue, outputSeen, toolObligation bool) bool {
	if value == nil || outputSeen || toolObligation || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return false
	}
	if value.TerminalReason != messages.TerminalReasonPartialOutput || value.OutputState != messages.TerminalOutputNone || value.Usage.CompletionTokens != 0 {
		return false
	}
	if value.TerminalReason == messages.TerminalReasonCancellation || strings.EqualFold(strings.TrimSpace(value.Status), "cancelled") {
		return false
	}
	return true
}

func (h *handle) latchProviderLiveness(failure session.LiveLivenessFailure) {
	if h == nil {
		return
	}
	err := &providerLivenessError{failure: failure}
	h.livenessMu.Lock()
	if h.livenessStopped || h.livenessFailure != nil {
		h.livenessMu.Unlock()
		return
	}
	copy := failure
	h.livenessFailure = &copy
	h.livenessErr = err
	h.livenessArmed = false
	h.livenessGeneration++
	timer := h.livenessTimer
	h.livenessTimer = nil
	h.livenessMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	h.publish(session.LiveEvent{
		Kind:      string(session.LiveEventLiveness),
		SessionID: h.request.SessionID,
		Error:     err,
		Liveness:  &copy,
		Critical:  true,
	}, false)
	h.Cancel(err)
}

func (h *handle) livenessFailureSnapshot() *session.LiveLivenessFailure {
	if h == nil {
		return nil
	}
	h.livenessMu.Lock()
	defer h.livenessMu.Unlock()
	if h.livenessFailure == nil {
		return nil
	}
	copy := *h.livenessFailure
	return &copy
}

func livenessFailureFromError(err error) *session.LiveLivenessFailure {
	if err == nil {
		return nil
	}
	var typed *providerLivenessError
	if !errors.As(err, &typed) || typed == nil {
		return nil
	}
	copy := typed.failure
	return &copy
}

// livenessToolExecutor marks local tool work as outside the provider progress
// budget. The next ordinary RESPONSE.CREATE admission arms the watchdog again
// after the tool result has been accepted by the provider.
type livenessToolExecutor struct {
	inner  messages.ToolExecutor
	handle *handle
}

func (e livenessToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e.handle != nil {
		e.handle.disarmProviderLiveness()
	}
	return e.inner.Execute(ctx, call)
}
