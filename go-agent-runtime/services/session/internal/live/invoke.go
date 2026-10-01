// Package live owns the complete live invocation boundary.
package live

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const liveMediaPumpCapacity = 3

// RunLive owns the complete invocation boundary for hosts that have local
// media. Device admission, provider startup, bounded event delivery, pump
// cancellation, and terminal joining stay together so a CLI transport cannot
// accidentally return while a capture/playback worker or provider recorder is
// still live.
func (s *Service) RunLive(ctx context.Context, options session.LiveRunOptions) error {
	if ctx == nil {
		return errors.New("live run context is required")
	}
	invocation, err := newLiveInvocation(s, ctx, options)
	if err != nil {
		return err
	}
	if err := invocation.start(ctx); err != nil {
		return invocation.closeAfterStartError(ctx, err)
	}
	return invocation.wait(ctx)
}

// liveInvocation is one Run call's state. Its methods receive the Run
// context explicitly; media pumps receive a derived pump context.
type liveInvocation struct {
	options                   session.LiveRunOptions
	handle                    session.LiveHandle
	device                    devices.Handle
	captureBoundaryOwned      bool
	endpoints                 sharedaudio.MediaEndpoints
	ports                     devices.MediaPorts
	captureInterruptionEvents <-chan session.LiveCapabilityEvent
	stopPumps                 context.CancelFunc
	pumps                     chan error
	count                     int
	// playbackDone closes when the playback pump started by
	// startPlaybackPump returns. It exists from the moment the pump is
	// scheduled, so a graceful drain joins a pump whose goroutine has not
	// begun yet instead of mistaking it for an idle device.
	playbackDone chan struct{}
}

func newLiveInvocation(s *Service, ctx context.Context, options session.LiveRunOptions) (*liveInvocation, error) {
	selectLegacyReplayFramePolicy(&options)
	liveHandle, err := openLiveHandle(s, ctx, options)
	if err != nil {
		return nil, err
	}
	captureBoundaryOwned := installCaptureBoundary(&options, liveHandle)
	invocation := &liveInvocation{
		options:              options,
		handle:               liveHandle,
		captureBoundaryOwned: captureBoundaryOwned,
		endpoints:            liveHandle.Media(),
	}
	if len(options.CaptureInterruptions) > 0 {
		runtimeHandle, ok := liveHandle.(*handle)
		if !ok {
			return invocation.closeWithError(ctx, errors.New("capture interruptions require the built-in live session owner"))
		}
		invocation.captureInterruptionEvents = runtimeHandle.configureCaptureInterruption(options.CaptureInterruptionTool)
	}
	if runtimeHandle, ok := liveHandle.(interface{ configureScheduledAudio(int, int) }); ok {
		runtimeHandle.configureScheduledAudio(len(options.CaptureTurns), captureResponseTarget(options.Request))
	}
	configureActiveScheduledAudio(liveHandle, options.AudioTurnAdmission == session.AudioTurnAdmissionBarge)
	if runtimeHandle, ok := liveHandle.(interface{ configureCaptureSource(bool) }); ok {
		runtimeHandle.configureCaptureSource(options.DeviceRequest.CaptureEnabled || len(options.CaptureTurns) > 0)
	}
	if runtimeHandle, ok := liveHandle.(interface{ configureMediaRequirements(bool, bool) }); ok {
		// Local capture is admitted through the loop's ordered audio ingress.
		// Only provider playback requires an inbound PCM media endpoint.
		runtimeHandle.configureMediaRequirements(options.DeviceRequest.PlaybackEnabled, false)
	}
	invocation.attachRecorder()
	if err := invocation.validateDeviceAdmission(); err != nil {
		return invocation.closeWithError(ctx, err)
	}
	if options.Devices == nil || !deviceRequestHasDirection(options.DeviceRequest) {
		return invocation, nil
	}
	device, err := options.Devices.Open(ctx, options.DeviceRequest)
	if err != nil {
		return invocation.closeWithError(ctx, fmt.Errorf("open live devices: %w", err))
	}
	if device == nil {
		return invocation.closeWithError(ctx, errors.New("open live devices: device service returned a nil handle"))
	}
	invocation.device = device
	invocation.ports = device.Media()
	invocation.bindPlaybackController()
	return invocation, nil
}
func openLiveHandle(s *Service, ctx context.Context, options session.LiveRunOptions) (session.LiveHandle, error) {
	if ctx == nil {
		err := errors.New("live invocation context is required")
		return nil, errors.Join(err, finalizeRecorder(options.Recorder, ctx, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, finalizeRecorder(options.Recorder, ctx, err))
	}
	handle, err := s.openLive(ctx, options.Request, options.Metrics)
	if err != nil {
		return nil, errors.Join(err, finalizeRecorder(options.Recorder, ctx, err))
	}
	return handle, nil
}
func installCaptureBoundary(options *session.LiveRunOptions, handle session.LiveHandle) bool {
	if options == nil || handle == nil || options.DeviceRequest.FileInput == nil || len(options.CaptureCompleteControls) == 0 {
		return false
	}
	input := *options.DeviceRequest.FileInput
	priorBoundary := input.OnTurnBoundary
	controls := append([]session.LiveControl(nil), options.CaptureCompleteControls...)
	input.OnTurnBoundary = func(ctx context.Context) error {
		if priorBoundary != nil {
			if err := priorBoundary(ctx); err != nil {
				return err
			}
		}
		noteCaptureBoundary(handle)
		for _, control := range controls {
			if err := handle.Send(ctx, control); err != nil {
				return fmt.Errorf("capture boundary control %q: %w", control.Kind, err)
			}
		}
		return nil
	}
	options.DeviceRequest.FileInput = &input
	return true
}
func deviceRequestHasDirection(request devices.Request) bool {
	return request.CaptureEnabled || request.PlaybackEnabled
}

func (r mediaRequirements) satisfiedBy(endpoints sharedaudio.MediaEndpoints) bool {
	return (!r.inbound || endpoints.Inbound != nil) && (!r.outbound || endpoints.Outbound != nil)
}

func (h *handle) configureMediaRequirements(inbound, outbound bool) {
	h.mu.Lock()
	h.mediaRequirements = mediaRequirements{inbound: inbound, outbound: outbound}
	h.mu.Unlock()
}

func (i *liveInvocation) attachRecorder() {
	if i == nil || i.options.Recorder == nil {
		return
	}
	setter, ok := i.handle.(interface{ setRecorder(session.LiveRecorder) })
	if ok {
		setter.setRecorder(i.options.Recorder)
	}
}

func (i *liveInvocation) validateDeviceAdmission() error {
	if i == nil {
		return errors.New("live invocation is unavailable")
	}
	if (len(i.options.CaptureTurns) > 0 || len(i.options.CaptureInterruptions) > 0) && i.options.Devices == nil {
		return errors.New("finite capture inputs require a device service")
	}
	return nil
}

func (i *liveInvocation) closeWithError(ctx context.Context, runErr error) (*liveInvocation, error) {
	if i == nil {
		return nil, runErr
	}
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	handleErr := i.handle.Close()
	result := errors.Join(runErr, deviceErr, handleErr)
	return nil, errors.Join(result, finalizeRecorder(i.options.Recorder, ctx, result))
}

func (i *liveInvocation) start(ctx context.Context) error {
	if i == nil || i.handle == nil {
		return errors.New("live invocation handle is unavailable")
	}
	if err := i.handle.Start(ctx); err != nil {
		return err
	}
	if ready, ok := i.handle.(interface{ waitReplayReady(context.Context) error }); ok {
		if err := ready.waitReplayReady(ctx); err != nil {
			return err
		}
	}
	if err := waitForOpeningContent(i.handle, ctx); err != nil {
		return err
	}
	pumpCtx, stopPumps := context.WithCancel(ctx)
	i.stopPumps = stopPumps
	return i.startPumps(pumpCtx)
}

func (i *liveInvocation) startPumps(ctx context.Context) error {
	if i == nil {
		return nil
	}
	if len(i.options.CaptureInterruptions) > 0 {
		if i.endpoints.Outbound == nil {
			return errors.New("capture interruptions require a live provider outbound media endpoint")
		}
		if i.captureInterruptionEvents == nil {
			return errors.New("capture interruptions require browser invocation events")
		}
	}
	i.startCapturePump(ctx)
	i.startCaptureInterruptionPump(ctx)
	i.startPlaybackPump(ctx)
	return nil
}

func (i *liveInvocation) startCapturePump(ctx context.Context) {
	if len(i.options.CaptureTurns) > 0 {
		if !i.captureOutboundAvailable() {
			i.handle.Cancel(errors.New("live provider has no audio input path"))
			return
		}
		i.startPump(ctx, "capture", i.runCaptureTurns)
		return
	}
	if i.ports.Capture == nil {
		return
	}
	if !i.captureOutboundAvailable() {
		i.handle.Cancel(errors.New("live provider has no audio input path"))
		return
	}
	target := i.captureOutbound()
	i.startPump(ctx, "capture", func(ctx context.Context) error {
		return i.ports.Capture.Pump(ctx, target)
	})
}

func (i *liveInvocation) captureOutboundAvailable() bool {
	if i == nil {
		return false
	}
	if _, ok := i.handle.(sessionAudioInputSender); ok {
		return true
	}
	return i.endpoints.Outbound != nil
}

func (i *liveInvocation) startCaptureInterruptionPump(ctx context.Context) {
	if i == nil || len(i.options.CaptureInterruptions) == 0 {
		return
	}
	i.startPump(ctx, "capture interruption", i.runCaptureInterruptions)
}

func (i *liveInvocation) startPump(ctx context.Context, name string, run func(context.Context) error) {
	if i == nil || run == nil {
		return
	}
	if i.pumps == nil {
		i.pumps = make(chan error, liveMediaPumpCapacity)
	}
	i.count++
	go i.runPump(ctx, name, run)
}

func (i *liveInvocation) runPump(ctx context.Context, name string, run func(context.Context) error) {
	pumpErr := run(ctx)
	if name == "capture" {
		pumpErr = i.completeCapturePump(ctx, pumpErr)
	}
	if shouldCancelMediaPumpFor(name, pumpErr, ctx) {
		i.handle.Cancel(fmt.Errorf("%s media pump: %w", name, pumpErr))
	}
	i.pumps <- pumpErr
}

func (i *liveInvocation) completeCapturePump(ctx context.Context, pumpErr error) error {
	if pumpErr != nil || len(i.options.CaptureTurns) > 0 {
		return pumpErr
	}
	if i.captureBoundaryOwned || len(i.options.CaptureCompleteControls) == 0 {
		if marker, ok := i.handle.(interface{ markCaptureComplete() }); ok {
			marker.markCaptureComplete()
		}
		return nil
	}
	noteCaptureBoundary(i.handle)
	for _, control := range i.options.CaptureCompleteControls {
		if err := i.handle.Send(ctx, control); err != nil {
			return fmt.Errorf("capture completion control %q: %w", control.Kind, err)
		}
	}
	if marker, ok := i.handle.(interface{ markCaptureComplete() }); ok {
		marker.markCaptureComplete()
	}
	return nil
}

func shouldCancelMediaPump(pumpErr error, ctx context.Context) bool {
	if isExpectedMediaPumpError(pumpErr) {
		return false
	}
	return ctx == nil || ctx.Err() == nil
}

func isExpectedMediaPumpError(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, session.ErrLiveClosed) ||
		errors.Is(err, devicert.ErrRTCDeviceSourceClosed) || errors.Is(err, devicert.ErrRTCDeviceSinkClosed) ||
		errors.Is(err, sharedaudio.ErrClosed) || errors.Is(err, sharedaudio.ErrSessionMediaClosed)
}

func (i *liveInvocation) wait(ctx context.Context) error {
	waitResult := make(chan error, 1)
	go func() { waitResult <- i.handle.Wait() }()
	events := i.handle.Events()
	var sinkErr error
	for {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if i.options.Events != nil && sinkErr == nil {
				if err := i.options.Events.Publish(ctx, event); err != nil {
					sinkErr = fmt.Errorf("publish live event: %w", err)
					i.handle.Cancel(sinkErr)
				}
			}
		case waitErr := <-waitResult:
			drainLiveEvents(events, i.options.Events, ctx, &sinkErr, i.handle)
			return i.finish(ctx, waitErr, sinkErr)
		}
	}
}

func (i *liveInvocation) finish(ctx context.Context, waitErr, sinkErr error) error {
	if i == nil {
		return errors.New("live invocation is unavailable")
	}
	var playbackErr error
	if shouldDrainPlayback(ctx, waitErr) {
		playbackErr = i.drainInvocationPlayback(ctx)
	}
	if i.stopPumps != nil {
		i.stopPumps()
	}
	// A capture worker may be blocked in a caller-provided stream read after a
	// provider-owned terminal boundary. Close the admitted device handle before
	// joining media workers so process-owned sources can interrupt that read;
	// graceful playback has already drained above, and cancellation paths do not
	// drain by design.
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	var pumpErr error
	for range i.count {
		candidate := <-i.pumps
		if !isExpectedMediaPumpError(candidate) {
			pumpErr = errors.Join(pumpErr, candidate)
		}
	}
	handleErr := i.handle.Close()
	result := errors.Join(waitErr, sinkErr, pumpErr, playbackErr, deviceErr, handleErr)
	return errors.Join(result, finalizeRecorder(i.options.Recorder, ctx, result))
}

func requestedTerminalError(s finishState) error {
	err := s.requestedErr
	if s.toolResultErr != nil && contextOnlyOrNil(s.requestedErr) {
		err = errors.Join(err, s.toolResultErr)
	}
	if s.providerErr != nil && !isContextTermination(s.providerErr) && !errors.Is(err, s.providerErr) {
		err = errors.Join(err, fmt.Errorf("session error: %w", s.providerErr))
	}
	return err
}
