package live

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func (h *handle) finalizeDuration(err error) error {
	h.mu.Lock()
	durationController := h.durationController
	h.mu.Unlock()
	if durationController != nil {
		_, durationErr := durationController.Finalize(context.WithoutCancel(h.evidenceContext()), sessionduration.FinalizeRequest{Primary: err})
		return durationErr
	}
	return err
}

func (h *handle) joinPumpError(err error) error {
	h.mu.Lock()
	if !isContextTermination(h.pumpErr) {
		err = errors.Join(err, h.pumpErr)
	}
	h.mu.Unlock()
	return err
}

// observeProviderDispatch arms the participant watchdog when the loop admits
// an operation that asks the provider to produce a response. The provider may
// emit MESSAGE.START asynchronously, so arming at dispatch closes the gap
// between a successful input admission and the first provider observation.
func (h *handle) observeProviderDispatch(msg messages.StreamMessage) {
	if h == nil {
		return
	}
	h.mu.Lock()
	durationController := h.durationController
	h.mu.Unlock()
	if durationController != nil {
		durationController.Observe(msg)
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
	if durationController != nil && (msg.Type == messages.StreamTypeMessageEnd || !acknowledgement) {
		durationController.ExpectProviderProgress()
	}
}

func (h *handle) observeRateLimit(loop *agentloop.AgentLoop, msg messages.StreamMessage) {
	if h == nil || loop == nil || msg.Type != messages.StreamTypeMessageEnd {
		return
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok {
		return
	}
	h.mu.Lock()
	controller := h.durationController
	h.mu.Unlock()
	if controller == nil {
		return
	}
	controller.Retry(sessionduration.RetryRequest{
		Terminal: terminal,
		Dispatch: func(ctx context.Context) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeResponseCreate,
				Value: messages.NewResponseCreateValue(),
			})
		},
	})
}

type timedToolExecutor struct {
	inner     messages.ToolExecutor
	scheduler platformclock.Scheduler
	timeout   time.Duration
}

func newTimedToolExecutor(inner messages.ToolExecutor, scheduler platformclock.Scheduler, timeout time.Duration) messages.ToolExecutor {
	if inner == nil || timeout <= 0 {
		return inner
	}
	return timedToolExecutor{inner: inner, scheduler: scheduler, timeout: timeout}
}

func (e timedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return messages.ToolCallResponse{}, err
	}
	if e.scheduler == nil {
		return messages.ToolCallResponse{}, session.ErrLiveSchedulerUnavailable
	}
	toolCtx, cancel := e.scheduler.WithTimeout(ctx, e.timeout)
	defer cancel()
	response, err := e.inner.Execute(toolCtx, call)
	if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return response, errors.Join(session.ErrLiveToolExecutionTimeout, err)
	}
	return response, err
}

func (h *handle) openingAdmissionRequired() bool {
	return h != nil && len(h.request.OpeningContentParts) > 0
}

func newScheduledAudioIncompleteError(scheduled, dispatched, completed int, terminal *messages.SessionCloseValue) error {
	if scheduled <= 0 {
		return nil
	}
	if completed < 0 {
		completed = 0
	}
	if completed > scheduled {
		completed = scheduled
	}
	if dispatched > scheduled {
		dispatched = scheduled
	}
	if completed >= scheduled && dispatched >= scheduled {
		return nil
	}
	incomplete := &session.LiveScheduledAudioIncompleteError{Completed: completed, Dispatched: dispatched, Scheduled: scheduled}
	if terminal != nil {
		incomplete.ProviderStatus = terminal.Classification
		incomplete.ProviderDetails = terminal.Reason
	}
	return incomplete
}

func (h *handle) markOpeningAdmitted(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if err != nil && h.openingAdmissionErr == nil {
		h.openingAdmissionErr = err
	}
	h.mu.Unlock()
	h.openingReadyOnce.Do(func() { close(h.openingReady) })
}

func (h *handle) waitOpeningReady(ctx context.Context) error {
	if !h.openingAdmissionRequired() {
		return nil
	}
	if ctx == nil {
		return errors.New("opening admission context is required")
	}
	select {
	case <-h.openingReady:
		h.mu.Lock()
		err := h.openingAdmissionErr
		h.mu.Unlock()
		return err
	case <-h.done:
		h.mu.Lock()
		err := h.terminalErr
		h.mu.Unlock()
		if err != nil {
			return err
		}
		return session.ErrLiveClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}
