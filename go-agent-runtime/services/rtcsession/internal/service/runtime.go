package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

type runtime struct {
	selection     rtcsession.SessionRuntimeSelection
	components    rtcsession.SessionRTCComponents
	metricSampler observability.MetricSampler
	logger        observability.Logger

	startMu sync.Mutex
	mu      sync.Mutex

	started   bool
	failed    bool
	startErr  error
	closed    bool
	startDone chan struct{}
	cancel    context.CancelFunc

	signaling rtc.Signaling
	dataPlane rtcsession.SessionRTCDataPlane
	media     sharedaudio.InboundMedia

	closeOnce sync.Once
	closeErr  error
}

var _ rtcsession.SessionRTCRuntime = (*runtime)(nil)

type startResources struct {
	signaling rtc.Signaling
	dataPlane rtcsession.SessionRTCDataPlane
	media     sharedaudio.InboundMedia
	cancel    context.CancelFunc
}

func (r *runtime) Start(ctx context.Context) (rtcsession.SessionRTCDataPlane, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if dataPlane, err, handled := r.startState(); handled {
		return dataPlane, err
	}
	runCtx, cancel, startDone := r.beginStart(ctx)
	defer r.finishStart(startDone)
	resources := startResources{cancel: cancel}
	if phase, err := r.acquire(runCtx, &resources); err != nil {
		return r.failStart(resources, phase, err)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.failStart(resources, "start", rtcsession.ErrSessionRTCRuntimeClosed)
	}
	r.signaling, r.dataPlane, r.media = resources.signaling, resources.dataPlane, resources.media
	r.started, r.cancel = true, cancel
	r.mu.Unlock()
	r.observe("started", "attach media source", "info")
	return resources.dataPlane, nil
}

func (r *runtime) startState() (rtcsession.SessionRTCDataPlane, error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, wrapError("start", rtcsession.ErrSessionRTCRuntimeClosed), true
	}
	if r.started {
		return r.dataPlane, nil, true
	}
	if r.failed {
		return nil, r.startErr, true
	}
	return nil, nil, false
}

func (r *runtime) beginStart(ctx context.Context) (context.Context, context.CancelFunc, chan struct{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	startDone := make(chan struct{})
	r.mu.Lock()
	r.cancel, r.startDone = cancel, startDone
	r.mu.Unlock()
	return runCtx, cancel, startDone
}

func (r *runtime) finishStart(startDone chan struct{}) {
	r.mu.Lock()
	if r.startDone == startDone {
		r.startDone = nil
		close(startDone)
	}
	r.mu.Unlock()
}

func (r *runtime) acquire(ctx context.Context, resources *startResources) (string, error) {
	signaling, err := r.components.ResolveSignaling(ctx, r.selection.SignalingEndpoint)
	resources.signaling = signaling
	if err != nil {
		return "resolve signaling", err
	}
	if signaling == nil {
		return "resolve signaling", rtcsession.ErrSessionRTCRuntimeUnavailable
	}
	dataPlane, err := r.components.NewDataPlane(ctx, signaling)
	resources.dataPlane = dataPlane
	if err != nil {
		return "create RTC peer/data path", err
	}
	if dataPlane == nil {
		return "create RTC peer/data path", rtcsession.ErrSessionRTCDataPlaneUnavailable
	}
	media, err := r.components.OpenMediaSource(ctx, r.selection.MediaSource)
	resources.media = media
	if err != nil {
		return "open media source", err
	}
	if media == nil {
		return "open media source", rtcsession.ErrSessionRTCRuntimeUnavailable
	}
	if err := dataPlane.AttachInboundMedia(ctx, media); err != nil {
		return "attach media source", err
	}
	return "", nil
}

func (r *runtime) failStart(resources startResources, phase string, err error) (rtcsession.SessionRTCDataPlane, error) {
	wrapped := wrapError(phase, err)
	if resources.cancel != nil {
		resources.cancel()
	}
	if closeErr := closeResources(resources.media, resources.dataPlane, resources.signaling); closeErr != nil {
		wrapped = errors.Join(wrapped, wrapError("cleanup after "+phase, closeErr))
	}
	r.mu.Lock()
	r.failed, r.startErr, r.cancel = true, wrapped, nil
	r.mu.Unlock()
	r.observe("start_failed", phase, "error")
	return nil, wrapped
}

func (r *runtime) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		cancel, startDone := r.cancel, r.startDone
		media, dataPlane, signaling := r.media, r.dataPlane, r.signaling
		r.media, r.dataPlane, r.signaling, r.cancel = nil, nil, nil, nil
		r.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if startDone != nil {
			<-startDone
		}
		r.closeErr = closeResources(media, dataPlane, signaling)
		level := "info"
		if r.closeErr != nil {
			level = "error"
		}
		r.observe("closed", "close", level)
	})
	return r.closeErr
}

func (r *runtime) observe(event, phase, level string) {
	if r == nil {
		return
	}
	fields := observability.Fields{"transport": r.selection.Transport, "phase": phase, "event": event}
	_ = observability.TrySample(context.Background(), r.metricSampler, observability.MetricSample{
		Name: "session.rtc.lifecycle", Kind: "counter", Value: 1, Unit: "events", Fields: fields,
	})
	_ = observability.TryLog(context.Background(), r.logger, observability.LogRecord{
		Level: level, Message: "session RTC runtime " + event, Fields: fields,
	})
}

func wrapError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return &rtcsession.SessionRTCRuntimeError{Phase: phase, Err: err}
}

func closeResources(media sharedaudio.InboundMedia, dataPlane rtcsession.SessionRTCDataPlane, signaling rtc.Signaling) error {
	return errors.Join(closeResource(media), closeResource(dataPlane), closeResource(signaling))
}

func closeResource(resource interface{ Close() error }) error {
	if resource == nil {
		return nil
	}
	return resource.Close()
}
