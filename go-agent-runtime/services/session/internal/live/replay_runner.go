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
	case <-ctx.Done():
		return ctx.Err()
	}
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
	if err := drainPlayback(i.ctx, i.ports.Playback, i.options.PlaybackDrainTimeout); err != nil {
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
