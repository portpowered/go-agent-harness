package live

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func (h *handle) runReplay(ctx context.Context) {
	defer h.runWG.Done()
	plan := h.request.ReplayPlan
	if plan == nil || len(plan.AudioTurns) == 0 {
		return
	}
	if err := h.prepareReplay(ctx); err != nil {
		h.cancelReplayOnError(ctx, "prepare replay", err)
		return
	}
	for turnIndex, turn := range plan.AudioTurns {
		if err := h.sendReplayTurn(ctx, turnIndex, turn); err != nil {
			h.cancelReplayOnError(ctx, "replay audio", err)
			return
		}
		// Keep each replay append behind the preceding response terminal so
		// provider admission preserves source-session causal ordering.
		if turnIndex+1 < len(plan.AudioTurns) {
			if err := h.waitReplayResponse(ctx, turnIndex+1); err != nil {
				h.cancelReplayOnError(ctx, fmt.Sprintf("wait for replay response %d", turnIndex+1), err)
				return
			}
		}
	}
	// Mark admission after every captured turn crosses bounded ingress.
	h.markCaptureComplete()
}

func (h *handle) prepareReplay(ctx context.Context) error {
	if err := h.waitReplayReady(ctx); err != nil {
		return fmt.Errorf("wait for replay provider readiness: %w", err)
	}
	if err := h.media.WaitReady(ctx); err != nil {
		return fmt.Errorf("wait for replay media: %w", err)
	}
	return nil
}

func (h *handle) sendReplayTurn(ctx context.Context, turnIndex int, turn session.LiveReplayAudioTurn) error {
	for chunkIndex, samples := range turn.Chunks {
		if len(samples) == 0 {
			continue
		}
		if err := h.media.Endpoints().Outbound.WriteFrame(ctx, sharedaudio.PCMFrame{Samples: samples}); err != nil {
			return fmt.Errorf("send replay audio turn %d chunk %d: %w", turnIndex+1, chunkIndex+1, err)
		}
	}
	if err := h.Send(ctx, session.LiveControl{Kind: session.LiveControlAudioCommit}); err != nil {
		return fmt.Errorf("commit replay audio turn %d: %w", turnIndex+1, err)
	}
	return nil
}

func (h *handle) cancelReplayOnError(ctx context.Context, operation string, err error) {
	if err == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	h.Cancel(fmt.Errorf("%s: %w", operation, err))
}

func (h *handle) waitReplayReady(ctx context.Context) error {
	if h == nil || h.request.ReplayPlan == nil || !h.request.ReplayPlan.WaitForSessionUpdated {
		return nil
	}
	if ctx == nil {
		return errors.New("replay readiness context is required")
	}
	select {
	case <-h.replayReady:
		return nil
	case <-h.done:
		// The session can end before session.updated arrives, for example when
		// strict replay rejects the opening message. Report that terminal
		// result instead of waiting for readiness that can no longer come.
		return h.terminalResult()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *handle) terminalResult() error {
	h.mu.Lock()
	err := h.terminalErr
	h.mu.Unlock()
	if err != nil {
		return err
	}
	return session.ErrLiveClosed
}

func (h *handle) waitReplayResponse(ctx context.Context, target int) error {
	return h.waitForResponse(ctx, target)
}

// waitForResponse waits for the requested number of non-tool response
// terminals. It is shared by replay and finite multi-turn capture workers so
// a later source cannot overtake the response that closes the preceding turn.
func (h *handle) waitForResponse(ctx context.Context, target int) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("response wait context is required")
	}
	for {
		h.mu.Lock()
		if h.replayResponses >= target {
			h.mu.Unlock()
			return nil
		}
		wake := h.replayResponseWake
		h.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// waitForOpeningResponse waits until target assistant response terminals were
// observed and no provider tool call still owes its result or continuation.
// Unlike waitForResponse it does not depend on finite-response accounting, so
// it also holds for persistent (--wait-for-close) sessions. A provider close
// releases the wait; the capture admission guard then reports the incomplete
// scheduled audio instead of stalling until the duration bound.
func (h *handle) waitForOpeningResponse(ctx context.Context, target int) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("opening response context is required")
	}
	for {
		h.mu.Lock()
		wake := h.replayResponseWake
		h.mu.Unlock()
		if h.openingResponseSettled(target) {
			return nil
		}
		select {
		case <-wake:
		case <-h.terminalObserved:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// wakeResponseWaiters releases every goroutine blocked on a response boundary
// so it re-evaluates its condition against the state just observed.
func (h *handle) wakeResponseWaiters() {
	h.mu.Lock()
	close(h.replayResponseWake)
	h.replayResponseWake = make(chan struct{})
	h.mu.Unlock()
}

// openingResponseSettled reports whether target assistant terminals were
// observed and every provider tool call has resolved its continuation.
func (h *handle) openingResponseSettled(target int) bool {
	h.mu.Lock()
	terminals := h.observedResponseTerminals
	h.mu.Unlock()
	if terminals < target {
		return false
	}
	h.toolMu.Lock()
	defer h.toolMu.Unlock()
	return len(h.toolContinuations) == 0
}

// waitForResponseBoundary includes partial assistant terminals produced by
// barge-in cancellation.
func (h *handle) waitForResponseBoundary(ctx context.Context, target int) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("response boundary context is required")
	}
	for {
		h.mu.Lock()
		ready := h.observedResponseTerminals >= target && !h.responseActive && !h.responsePending
		terminalWake, responseWake := h.responseTerminalWake, h.replayResponseWake
		h.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-terminalWake:
		case <-responseWake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// joinWorkerError adds a media worker failure to the invocation result unless
// it is a context termination or already produced that result, so one failure
// is reported once.
func joinWorkerError(err, workerErr error) error {
	if isContextTermination(workerErr) || errors.Is(err, workerErr) {
		return err
	}
	return errors.Join(err, workerErr)
}

const (
	// playbackQueueStallTimeout bounds a graceful drain whose device consumer
	// stops advancing the local playback queue.
	playbackQueueStallTimeout = 250 * time.Millisecond
	playbackQueuePollInterval = 5 * time.Millisecond
)

// drainInvocationPlayback joins the playback pump and then lets the device
// consumer empty the local queue, so closing the device does not discard the
// response tail. Native devices already drain inside the pump; a consumer
// that stops advancing is abandoned after a bounded stall.
func (i *liveInvocation) drainInvocationPlayback() error {
	if err := drainPlayback(i.ctx, i.ports.Playback, i.options.PlaybackDrainTimeout, i.playbackDone); err != nil {
		return err
	}
	if provider, ok := i.device.(devices.PlaybackStatsProvider); ok {
		waitForPlaybackQueue(i.ctx, provider)
	}
	return nil
}

func waitForPlaybackQueue(ctx context.Context, provider devices.PlaybackStatsProvider) {
	_, stats := provider.PlaybackStats()
	queued, progressed := stats.QueuedSamples, time.Now()
	for queued > 0 && time.Since(progressed) < playbackQueueStallTimeout {
		timer := time.NewTimer(playbackQueuePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		_, stats = provider.PlaybackStats()
		if stats.QueuedSamples < queued {
			progressed = time.Now()
		}
		queued = stats.QueuedSamples
	}
}

func (i *liveInvocation) startPlaybackPump() {
	if i.ports.Playback == nil {
		return
	}
	if i.endpoints.Inbound == nil {
		i.handle.Cancel(errors.New("live provider has no inbound media endpoint"))
		return
	}
	done := make(chan struct{})
	i.playbackDone = done
	i.startPump("playback", func(ctx context.Context) error {
		defer close(done)
		return i.ports.Playback.Pump(ctx, i.endpoints.Inbound)
	})
}

// drainPlayback joins a drain-capable playback pump. The device reports only
// a pump that has already begun, so pumpDone, which the invocation creates
// when it schedules the pump, also covers a pump goroutine that has not run
// yet; without it a fast graceful completion would close the device before
// the pump read any provider audio.
func drainPlayback(parent context.Context, playback devices.Playback, timeout time.Duration, pumpDone <-chan struct{}) error {
	if playback == nil {
		return nil
	}
	if parent == nil {
		return errors.New("live playback drain context is required")
	}
	drainer, ok := playback.(interface{ WaitForPump(context.Context) error })
	if !ok {
		return nil
	}
	if timeout == 0 {
		timeout = defaultPlaybackDrainTimeout
	}
	if timeout < 0 {
		return errors.New("live playback drain timeout must not be negative")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := drainer.WaitForPump(ctx); err != nil {
		return fmt.Errorf("drain live playback: %w", err)
	}
	if pumpDone == nil {
		return nil
	}
	select {
	case <-pumpDone:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("drain live playback: %w", ctx.Err())
	}
}

// noteCaptureBoundary records how many finite responses completed before a
// capture turn boundary reaches the provider. The provider may answer that
// boundary before the capture pump returns and marks capture complete, so the
// finish target must be taken here rather than at completion time.
func noteCaptureBoundary(value any) {
	if h, ok := value.(*handle); ok && h != nil {
		h.mu.Lock()
		h.captureBoundaryResponses, h.captureBoundaryNoted = h.replayResponses, true
		h.mu.Unlock()
	}
}

// captureResponseBaseLocked is the response count that precedes the final
// capture boundary. Without a noted boundary it falls back to the count at
// completion. The caller holds h.mu.
func (h *handle) captureResponseBaseLocked() int {
	if h.captureBoundaryNoted {
		return h.captureBoundaryResponses
	}
	return h.replayResponses
}
