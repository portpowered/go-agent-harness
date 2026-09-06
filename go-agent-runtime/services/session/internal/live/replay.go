package live

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// runCaptureTurns admits caller-owned finite audio into one persistent live
// provider session. It shares the replay boundary helpers below because both
// paths must serialize media and turn controls against provider responses.
func (i *liveInvocation) runCaptureTurns(ctx context.Context) error {
	if err := validateCaptureInvocation(i); err != nil {
		return err
	}
	admission, err := normalizeAudioTurnAdmission(i.options.AudioTurnAdmission)
	if err != nil {
		return err
	}
	responseTarget := captureResponseTarget(i.options.Request)
	for index, input := range i.options.CaptureTurns {
		if err := i.runCaptureTurn(ctx, index, input, admission); err != nil {
			return err
		}
		responseTarget++
		if err := i.waitForNextCaptureTurn(ctx, index, admission, responseTarget, len(i.options.CaptureTurns)); err != nil {
			return err
		}
	}
	if marker, ok := i.handle.(interface{ markCaptureComplete() }); ok {
		marker.markCaptureComplete()
	}
	return nil
}

func validateCaptureInvocation(i *liveInvocation) error {
	if i == nil || i.options.Devices == nil || i.endpoints.Outbound == nil {
		return errors.New("finite capture service is unavailable")
	}
	return nil
}

func captureResponseTarget(request session.LiveRequest) int {
	if openingMessageRequestsResponse(request) {
		return 1
	}
	return 0
}

func (i *liveInvocation) runCaptureTurn(ctx context.Context, index int, input devices.FileInput, admission session.AudioTurnAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := i.prepareCaptureTurn(ctx, index, admission); err != nil {
		return err
	}
	if err := i.captureFiniteTurn(ctx, index, input); err != nil {
		return err
	}
	return i.completeCaptureTurn(ctx, index)
}

func (i *liveInvocation) completeCaptureTurn(ctx context.Context, index int) error {
	for _, control := range i.options.CaptureCompleteControls {
		if err := i.handle.Send(ctx, control); err != nil {
			return fmt.Errorf("capture completion control %q for turn %d: %w", control.Kind, index+1, err)
		}
	}
	return nil
}

func (i *liveInvocation) prepareCaptureTurn(ctx context.Context, index int, admission session.AudioTurnAdmission) error {
	if index == 0 || admission != session.AudioTurnAdmissionBarge {
		return nil
	}
	if waiter, ok := i.handle.(interface {
		waitForResponseStart(context.Context) error
	}); ok {
		if err := waiter.waitForResponseStart(ctx); err != nil {
			return fmt.Errorf("wait for finite capture turn %d response start: %w", index+1, err)
		}
	}
	if !i.handleResponseIsActive() {
		return nil
	}
	if err := i.handle.Send(ctx, session.LiveControl{Kind: session.LiveControlResponseCancel}); err != nil {
		return fmt.Errorf("cancel response before finite capture turn %d: %w", index+1, err)
	}
	return nil
}

func (i *liveInvocation) captureFiniteTurn(ctx context.Context, index int, input devices.FileInput) error {
	request := i.options.DeviceRequest
	request.CaptureEnabled = true
	request.PlaybackEnabled = false
	request.FileInput = &input
	request.FileOutput = nil
	device, err := i.options.Devices.Open(ctx, request)
	if err != nil {
		return fmt.Errorf("open finite capture turn %d: %w", index+1, err)
	}
	if device == nil {
		return fmt.Errorf("open finite capture turn %d: device service returned a nil handle", index+1)
	}
	ports := device.Media()
	if ports.Capture == nil {
		closeErr := device.Close()
		return errors.Join(fmt.Errorf("open finite capture turn %d: device service returned no capture port", index+1), closeErr)
	}
	packet, packetErr := newFiniteTurnOutbound(i.captureOutbound())
	pumpErr := packetErr
	if pumpErr == nil {
		pumpErr = ports.Capture.Pump(ctx, packet)
	}
	if pumpErr == nil {
		pumpErr = packet.Flush(ctx)
	}
	closeErr := device.Close()
	if errors.Is(pumpErr, io.EOF) {
		pumpErr = nil
	}
	if pumpErr != nil {
		pumpErr = fmt.Errorf("finite capture turn %d: %w", index+1, pumpErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close finite capture turn %d: %w", index+1, closeErr)
	}
	return errors.Join(pumpErr, closeErr)
}

func (i *liveInvocation) waitForNextCaptureTurn(ctx context.Context, index int, admission session.AudioTurnAdmission, responseTarget, total int) error {
	if index+1 >= total || !shouldWaitForCaptureResponse(index, admission) {
		return nil
	}
	waiter, ok := i.handle.(interface {
		waitForResponse(context.Context, int) error
	})
	if !ok {
		return nil
	}
	if err := waiter.waitForResponse(ctx, responseTarget); err != nil {
		return fmt.Errorf("wait for finite capture turn %d response: %w", index+1, err)
	}
	return nil
}

func shouldWaitForCaptureResponse(index int, admission session.AudioTurnAdmission) bool {
	return admission == session.AudioTurnAdmissionCompletionGated || index > 0
}

func normalizeAudioTurnAdmission(value session.AudioTurnAdmission) (session.AudioTurnAdmission, error) {
	if value == "" {
		return session.AudioTurnAdmissionCompletionGated, nil
	}
	switch value {
	case session.AudioTurnAdmissionCompletionGated, session.AudioTurnAdmissionBarge:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported audio turn admission %q", value)
	}
}

const finiteTurnFrameBudget = 48_000

type sessionAudioInputSender interface {
	sendAudioInput(context.Context, []byte, messages.SessionAudioInputPolicy) error
}

type loopAudioOutbound struct {
	sender  sessionAudioInputSender
	onAdmit func(sharedaudio.PCMFrame)
}

func (i *liveInvocation) captureOutbound() sharedaudio.OutboundMedia {
	if i == nil {
		return nil
	}
	sender, ok := i.handle.(sessionAudioInputSender)
	if !ok || sender == nil {
		return i.endpoints.Outbound
	}
	return &loopAudioOutbound{sender: sender, onAdmit: i.captureAdmitted}
}

// sendAudioInput keeps local capture on the model runner's ordered ingress.
// Peer media in a room continues to use Gate directly so its explicit
// non-interrupting policy remains owned by the room graph.
func (h *handle) sendAudioInput(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	if h == nil {
		return errors.New("live audio input handle is unavailable")
	}
	h.mu.Lock()
	loop := h.loop
	started, closed := h.started, h.closed
	h.mu.Unlock()
	if !started || loop == nil {
		return session.ErrLiveNotStarted
	}
	if closed {
		return session.ErrLiveClosed
	}
	return loop.SendAudioInputWithPolicy(ctx, pcm, policy)
}

func (h *handle) recordCapturedAudio(frame sharedaudio.PCMFrame) {
	if h == nil {
		return
	}
	if observer := h.observationPort(); observer != nil {
		observer.QueueFrame(frame)
	}
}

func (o *loopAudioOutbound) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if o == nil || o.sender == nil {
		return errors.New("ordered audio input sender is unavailable")
	}
	if len(frame.Samples) == 0 {
		return sharedaudio.ErrSessionMediaEmptyFrame
	}
	if err := o.sender.sendAudioInput(ctx, codec.EncodePCM16(frame.Samples), messages.SessionAudioInputPolicyDefault); err != nil {
		return fmt.Errorf("admit ordered audio input: %w", err)
	}
	if o.onAdmit != nil {
		o.onAdmit(frame)
	}
	return nil
}

func (*loopAudioOutbound) Close() error { return nil }

func (i *liveInvocation) captureAdmitted(frame sharedaudio.PCMFrame) {
	if i == nil || i.handle == nil {
		return
	}
	if observer, ok := i.handle.(interface{ recordCapturedAudio(sharedaudio.PCMFrame) }); ok {
		observer.recordCapturedAudio(frame)
	}
}

func newFiniteTurnOutbound(target sharedaudio.OutboundMedia) (*sharedaudio.FrameAccumulator, error) {
	return sharedaudio.NewFrameAccumulator(target, finiteTurnFrameBudget)
}

func (i *liveInvocation) handleResponseIsActive() bool {
	if i == nil || i.handle == nil {
		return false
	}
	if snapshot, ok := i.handle.(interface{ responseIsActive() bool }); ok {
		return snapshot.responseIsActive()
	}
	return false
}

func openingMessageRequestsResponse(request session.LiveRequest) bool {
	if len(request.OpeningContentParts) > 0 {
		return request.OpeningMessageResponse != session.LiveOpeningMessageQueued
	}
	return request.OpeningPromptPresent || request.OpeningPrompt != ""
}

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
		// A capture can contain multiple audio turns. Keep the next append
		// behind the response terminal for this turn so the replay cursor and
		// provider response admission observe the same causal ordering as the
		// source session. Sending every turn as soon as its local input queue
		// accepts it lets a second response.create race the first response and
		// can cause the provider to close before publishing its deltas.
		if turnIndex+1 < len(plan.AudioTurns) {
			if err := h.waitReplayResponse(ctx, turnIndex+1); err != nil {
				h.cancelReplayOnError(ctx, fmt.Sprintf("wait for replay response %d", turnIndex+1), err)
				return
			}
		}
	}
	// Mark admission only after every captured turn has crossed the same
	// bounded media/control ingress. Marking before the loop lets the first
	// response's MESSAGE.END cancel replay while later recorded turns are
	// still waiting to be admitted.
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
