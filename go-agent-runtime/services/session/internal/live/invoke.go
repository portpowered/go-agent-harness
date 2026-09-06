// Package live owns the complete live invocation boundary.
package live

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

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
	if err := invocation.start(); err != nil {
		return invocation.closeAfterStartError(err)
	}
	return invocation.wait()
}

type liveInvocation struct {
	ctx                  context.Context
	options              session.LiveRunOptions
	handle               session.LiveHandle
	device               devices.Handle
	captureBoundaryOwned bool

	endpoints sharedaudio.MediaEndpoints
	ports     devices.MediaPorts
	pumpCtx   context.Context
	stopPumps context.CancelFunc
	pumps     chan error
	count     int
}

func newLiveInvocation(s *Service, ctx context.Context, options session.LiveRunOptions) (*liveInvocation, error) {
	handle, err := openLiveHandle(s, ctx, options)
	if err != nil {
		return nil, err
	}
	captureBoundaryOwned := installCaptureBoundary(&options, handle)
	invocation := &liveInvocation{
		ctx:                  ctx,
		options:              options,
		handle:               handle,
		captureBoundaryOwned: captureBoundaryOwned,
		endpoints:            handle.Media(),
	}
	if runtimeHandle, ok := handle.(interface{ configureScheduledAudio(int, int) }); ok {
		runtimeHandle.configureScheduledAudio(len(options.CaptureTurns), captureResponseTarget(options.Request))
	}
	if runtimeHandle, ok := handle.(interface{ configureCaptureSource(bool) }); ok {
		runtimeHandle.configureCaptureSource(options.DeviceRequest.CaptureEnabled || len(options.CaptureTurns) > 0)
	}
	invocation.attachRecorder()
	if err := invocation.validateDeviceAdmission(); err != nil {
		return invocation.closeWithError(err)
	}
	if options.Devices == nil || !deviceRequestHasDirection(options.DeviceRequest) {
		return invocation, nil
	}
	device, err := options.Devices.Open(ctx, options.DeviceRequest)
	if err != nil {
		return invocation.closeWithError(fmt.Errorf("open live devices: %w", err))
	}
	if device == nil {
		return invocation.closeWithError(errors.New("open live devices: device service returned a nil handle"))
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
	handle, err := s.OpenLive(ctx, options.Request)
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
	if len(i.options.CaptureTurns) > 0 && i.options.Devices == nil {
		return errors.New("finite capture turns require a device service")
	}
	return nil
}

func (i *liveInvocation) closeWithError(runErr error) (*liveInvocation, error) {
	if i == nil {
		return nil, runErr
	}
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	handleErr := i.handle.Close()
	result := errors.Join(runErr, deviceErr, handleErr)
	return nil, errors.Join(result, finalizeRecorder(i.options.Recorder, i.ctx, result))
}

func (i *liveInvocation) bindPlaybackController() {
	if i == nil || i.device == nil || i.endpoints.Inbound == nil {
		return
	}
	provider, ok := i.ports.Playback.(devices.PlaybackControllerProvider)
	if !ok {
		return
	}
	controlled, ok := i.endpoints.Inbound.(sharedaudio.PlaybackControlledInbound)
	if ok {
		controlled.SetPlaybackController(provider.PlaybackController())
	}
}

func (i *liveInvocation) start() error {
	if i == nil || i.handle == nil {
		return errors.New("live invocation handle is unavailable")
	}
	if err := i.handle.Start(i.ctx); err != nil {
		return err
	}
	if ready, ok := i.handle.(interface{ waitReplayReady(context.Context) error }); ok {
		if err := ready.waitReplayReady(i.ctx); err != nil {
			return err
		}
	}
	i.pumpCtx, i.stopPumps = context.WithCancel(i.ctx)
	i.startPumps()
	return nil
}

func (i *liveInvocation) startPumps() {
	if i == nil {
		return
	}
	i.startCapturePump()
	i.startPlaybackPump()
}

func (i *liveInvocation) startCapturePump() {
	if len(i.options.CaptureTurns) > 0 {
		if i.endpoints.Outbound == nil {
			i.handle.Cancel(errors.New("live provider has no outbound media endpoint"))
			return
		}
		i.startPump("capture", i.runCaptureTurns)
		return
	}
	if i.ports.Capture == nil {
		return
	}
	if i.endpoints.Outbound == nil {
		i.handle.Cancel(errors.New("live provider has no outbound media endpoint"))
		return
	}
	target := i.captureOutbound()
	i.startPump("capture", func(ctx context.Context) error {
		return i.ports.Capture.Pump(ctx, target)
	})
}

func (i *liveInvocation) startPlaybackPump() {
	if i.ports.Playback == nil {
		return
	}
	if i.endpoints.Inbound == nil {
		i.handle.Cancel(errors.New("live provider has no inbound media endpoint"))
		return
	}
	i.startPump("playback", func(ctx context.Context) error {
		return i.ports.Playback.Pump(ctx, i.endpoints.Inbound)
	})
}

func (i *liveInvocation) startPump(name string, run func(context.Context) error) {
	if i == nil || run == nil {
		return
	}
	if i.pumps == nil {
		i.pumps = make(chan error, 2)
	}
	i.count++
	go i.runPump(name, run)
}

func (i *liveInvocation) runPump(name string, run func(context.Context) error) {
	pumpErr := run(i.pumpCtx)
	if name == "capture" {
		pumpErr = i.completeCapturePump(pumpErr)
	}
	if shouldCancelMediaPump(pumpErr, i.pumpCtx) {
		i.handle.Cancel(fmt.Errorf("%s media pump: %w", name, pumpErr))
	}
	i.pumps <- pumpErr
}

func (i *liveInvocation) completeCapturePump(pumpErr error) error {
	if pumpErr != nil || len(i.options.CaptureTurns) > 0 {
		return pumpErr
	}
	if i.captureBoundaryOwned || len(i.options.CaptureCompleteControls) == 0 {
		if marker, ok := i.handle.(interface{ markCaptureComplete() }); ok {
			marker.markCaptureComplete()
		}
		return nil
	}
	for _, control := range i.options.CaptureCompleteControls {
		if err := i.handle.Send(i.pumpCtx, control); err != nil {
			return fmt.Errorf("capture completion control %q: %w", control.Kind, err)
		}
	}
	if marker, ok := i.handle.(interface{ markCaptureComplete() }); ok {
		marker.markCaptureComplete()
	}
	return nil
}

func shouldCancelMediaPump(pumpErr error, ctx context.Context) bool {
	if pumpErr == nil || errors.Is(pumpErr, io.EOF) {
		return false
	}
	return ctx == nil || ctx.Err() == nil
}

func (i *liveInvocation) wait() error {
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
				if err := i.options.Events.Publish(i.ctx, event); err != nil {
					sinkErr = fmt.Errorf("publish live event: %w", err)
					i.handle.Cancel(sinkErr)
				}
			}
		case waitErr := <-waitResult:
			drainLiveEvents(events, i.options.Events, i.ctx, &sinkErr, i.handle)
			return i.finish(waitErr, sinkErr)
		}
	}
}

func (i *liveInvocation) finish(waitErr, sinkErr error) error {
	if i == nil {
		return errors.New("live invocation is unavailable")
	}
	var playbackErr error
	if shouldDrainPlayback(i.ctx, waitErr) {
		playbackErr = drainPlayback(i.ctx, i.ports.Playback, i.options.PlaybackDrainTimeout)
	}
	if i.stopPumps != nil {
		i.stopPumps()
	}
	var pumpErr error
	for count := 0; count < i.count; count++ {
		candidate := <-i.pumps
		if candidate != nil && !errors.Is(candidate, io.EOF) && !errors.Is(candidate, context.Canceled) && !errors.Is(candidate, context.DeadlineExceeded) {
			pumpErr = errors.Join(pumpErr, candidate)
		}
	}
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	handleErr := i.handle.Close()
	result := errors.Join(waitErr, sinkErr, pumpErr, playbackErr, deviceErr, handleErr)
	return errors.Join(result, finalizeRecorder(i.options.Recorder, i.ctx, result))
}

func (i *liveInvocation) closeAfterStartError(startErr error) error {
	if i == nil {
		return startErr
	}
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	handleErr := i.handle.Close()
	result := errors.Join(startErr, deviceErr, handleErr)
	return errors.Join(result, finalizeRecorder(i.options.Recorder, i.ctx, result))
}

func finalizeRecorder(recorder session.LiveRecorder, ctx context.Context, runErr error) error {
	if recorder == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("live recorder finalization context is required")
	}
	// Evidence finalization is a lifecycle join. The recorder receives the
	// invocation result but must still publish a partial bundle when the parent
	// context was canceled, so retain the parent's values while removing only
	// its cancellation signal.
	return recorder.Finalize(context.WithoutCancel(ctx), runErr)
}

func drainPlayback(parent context.Context, playback devices.Playback, timeout time.Duration) error {
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
	return nil
}
