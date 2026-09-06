package live

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func (h *handle) Send(ctx context.Context, control session.LiveControl) error {
	if h == nil {
		return session.ErrLiveClosed
	}
	if ctx == nil {
		return errors.New("live control context is required")
	}
	h.mu.Lock()
	if !h.started || h.loop == nil {
		h.mu.Unlock()
		return session.ErrLiveNotStarted
	}
	if h.closed {
		h.mu.Unlock()
		return session.ErrLiveClosed
	}
	loop := h.loop
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.refreshLiveTools(ctx, loop); err != nil {
		return err
	}
	// Close is teardown, so it must cancel the provider and media bridges before
	// waiting on any in-flight media write. Waiting behind a blocked writer here
	// would make a close control unable to stop that writer.
	if control.Kind == session.LiveControlClose {
		h.Cancel(context.Canceled)
		return nil
	}

	// Reserve the control's place in the same admission sequence used by PCM
	// frames and automatic provider sends. Reservation is non-blocking; waiting
	// for an earlier operation while holding a mutex would deadlock the
	// agent-loop runner when that earlier operation is itself a provider send.
	ackID, ack, err := h.media.RegisterAck()
	if err != nil {
		return err
	}
	event, err := liveControlEvent(control)
	if err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	event.ActorProvidedID = ackID
	if err := loop.SendSessionEvent(ctx, event); err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return fmt.Errorf("live provider rejected control %q", control.Kind)
		}
		return nil
	case <-ctx.Done():
		h.media.CancelAck(ackID)
		return ctx.Err()
	}
}

// refreshLiveTools publishes a participant's current tool surface before a
// new control enters the provider wire. The update uses the same marked
// admission barrier as text/commit/cancel, so a page-discovery refresh cannot
// overtake already admitted PCM or be overtaken by the following control.
func (h *handle) refreshLiveTools(ctx context.Context, loop *agentloop.AgentLoop) error {
	h.mu.Lock()
	refresh := h.capabilityRefresh
	h.mu.Unlock()
	if refresh == nil {
		return nil
	}
	h.capabilityMu.Lock()
	defer h.capabilityMu.Unlock()
	// A concurrent Send may have refreshed the definitions while this call was
	// waiting for the capability mutex. Re-read the current snapshot before
	// deciding whether another provider update is needed.
	h.mu.Lock()
	current := append([]messages.ToolDefinition(nil), h.toolDefinitions...)
	h.mu.Unlock()
	refreshed, err := refresh(ctx)
	if err != nil {
		return fmt.Errorf("refresh live capabilities: %w", err)
	}
	refreshed = messages.CanonicalToolDefinitions(refreshed)
	if reflect.DeepEqual(current, refreshed) {
		return nil
	}
	ackID, ack, err := h.media.RegisterAck()
	if err != nil {
		return err
	}
	event := messages.StreamMessage{
		Type:            messages.StreamTypeSessionUpdate,
		Value:           messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: refreshed}),
		ActorProvidedID: ackID,
	}
	if err := loop.SendSessionEvent(ctx, event); err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return errors.New("live provider rejected capability refresh")
		}
		h.mu.Lock()
		h.toolDefinitions = append([]messages.ToolDefinition(nil), refreshed...)
		if h.request.Capabilities != nil {
			binding := *h.request.Capabilities
			binding.Definitions = append([]messages.ToolDefinition(nil), refreshed...)
			h.request.Capabilities = &binding
		}
		h.mu.Unlock()
		return nil
	case <-ctx.Done():
		h.media.CancelAck(ackID)
		return ctx.Err()
	}
}

func liveControlEvent(control session.LiveControl) (messages.StreamMessage, error) {
	switch control.Kind {
	case session.LiveControlText:
		// The session runner's text admission path ultimately sends this same
		// provider TEXT.DELTA event. Using its ordered session ingress lets the
		// live boundary wait for provider admission without adding a second
		// untracked text queue.
		return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(control.Text)}, nil
	case session.LiveControlAudioCommit:
		return messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, nil
	case session.LiveControlResponseCancel:
		return messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}, nil
	case session.LiveControlResponseCreate:
		return messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}, nil
	case session.LiveControlClose:
		return messages.StreamMessage{}, errors.New("close control is handled by the live lifecycle")
	default:
		return messages.StreamMessage{}, fmt.Errorf("unsupported live control %q", control.Kind)
	}
}

func (h *handle) Cancel(err error) {
	if h == nil {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	h.mu.Lock()
	h.cancelRequested = true
	// Preserve the first cancellation cause. Provider/media teardown often
	// reports context.Canceled after a typed liveness, persistence, or device
	// failure; replacing that cause would make the public Wait result lose the
	// actionable root error.
	if h.cancelCause == nil {
		h.cancelCause = err
	}
	cancel := h.cancel
	started := h.started
	h.mu.Unlock()
	if started && cancel != nil {
		cancel(err)
	}
}

func (h *handle) stopGracefully() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed || h.gracefulStop || h.cancelRequested {
		h.mu.Unlock()
		return
	}
	h.gracefulStop = true
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel(nil)
	}
}

func (h *handle) Wait() error {
	if h == nil {
		return session.ErrLiveNotStarted
	}
	h.mu.Lock()
	started := h.started
	startDone := h.startDone
	done := h.done
	h.mu.Unlock()
	if !started {
		return session.ErrLiveNotStarted
	}
	<-startDone
	<-done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminalErr
}

func (h *handle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		started := h.started
		done := h.done
		h.mu.Unlock()
		if started {
			<-done
		}
		return nil
	}
	h.closed = true
	started := h.started
	cancel := h.cancel
	if started {
		h.cancelRequested = true
		// Close is teardown. Preserve a typed cause already recorded by the
		// session (for example liveness, replay, or device failure) so the
		// provider/media cancellation it triggers cannot mask the first fault.
		if h.cancelCause == nil {
			h.cancelCause = context.Canceled
		}
	}
	h.mu.Unlock()
	if started && cancel != nil {
		cancel(context.Canceled)
	}
	mediaErr := h.media.Close()
	if !started {
		close(h.startDone)
		h.finish(nil)
		return mediaErr
	}
	<-h.done
	return mediaErr
}

// finishToolContinuations closes the bookkeeping loop for an accepted tool
// result. A provider MESSAGE.END without observable continuation output is a
// failed continuation even when the transport itself closed cleanly.
func (h *handle) finishToolContinuations(msg messages.StreamMessage) (error, bool) {
	if h == nil {
		return nil, false
	}
	status, code, detail := continuationStatus(msg)
	h.toolMu.Lock()
	image, tools, completed := h.collectContinuationFailures(status, code, detail, msg.Value)
	failure := continuationFailure(image, tools)
	if failure != nil && h.continuationErr == nil {
		h.continuationErr = failure
	}
	stored := h.continuationErr
	h.toolMu.Unlock()
	return stored, completed
}

type continuationFailures struct {
	ids      []string
	statuses map[string]string
	codes    map[string]string
	details  map[string]string
}

func continuationStatus(msg messages.StreamMessage) (string, string, string) {
	value, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || value == nil {
		return "", "", ""
	}
	detail := strings.TrimSpace(value.StatusDetails)
	if detail == "" {
		detail = strings.TrimSpace(value.ProviderErrorMessage)
	}
	return strings.TrimSpace(value.Status), strings.TrimSpace(value.ProviderErrorCode), detail
}

func (h *handle) collectContinuationFailures(status, code, detail string, raw any) (continuationFailures, continuationFailures, bool) {
	image := newContinuationFailures()
	tools := newContinuationFailures()
	completed := false
	value, ok := raw.(*messages.MessageEndValue)
	if !ok {
		value = nil
	}
	for callID, state := range h.toolContinuations {
		if !state.resultAccepted || !state.continuationRequested {
			continue
		}
		state.status, state.code, state.detail = status, code, detail
		if continuationFailed(value, state.outputObserved) {
			target := &tools
			if strings.EqualFold(strings.TrimSpace(state.name), "read_image") {
				target = &image
			}
			target.add(callID, status, code, detail)
			continue
		}
		completed = true
		delete(h.toolContinuations, callID)
	}
	return image, tools, completed
}

func newContinuationFailures() continuationFailures {
	return continuationFailures{
		ids: make([]string, 0), statuses: make(map[string]string),
		codes: make(map[string]string), details: make(map[string]string),
	}
}

func (f *continuationFailures) add(callID, status, code, detail string) {
	if f == nil {
		return
	}
	f.ids = append(f.ids, callID)
	f.statuses[callID], f.codes[callID], f.details[callID] = status, code, detail
}

func continuationFailure(image, tools continuationFailures) error {
	var failure error
	if len(image.ids) > 0 {
		sort.Strings(image.ids)
		failure = &session.LiveImageContinuationError{
			CallIDs: image.ids, ProviderStatuses: image.statuses,
			ProviderCodes: image.codes, ProviderDetails: image.details,
		}
	}
	if len(tools.ids) == 0 {
		return failure
	}
	sort.Strings(tools.ids)
	toolFailure := &session.LiveToolContinuationError{
		CallIDs: tools.ids, ProviderStatuses: tools.statuses,
		ProviderCodes: tools.codes, ProviderDetails: tools.details,
	}
	if failure == nil {
		return toolFailure
	}
	return errors.Join(failure, toolFailure)
}

func continuationFailed(value *messages.MessageEndValue, outputObserved bool) bool {
	if !outputObserved {
		return true
	}
	if value == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(value.Status))
	return status == "failed" || status == "cancelled" || status == "canceled" || status == "incomplete" || status == "error"
}
