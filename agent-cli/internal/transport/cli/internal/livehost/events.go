package livehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSessionTrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type publicTraceDeviceService struct {
	inner runtimeDevices.Service
	trace *publicTraceRun
}

type traceCapturePreGateSetter interface {
	SetPreGateSamplesObserver(func(int, []int16))
}

type traceCaptureUploadedSetter interface {
	SetUploadedSamplesObserver(func(int, []int16))
}

type tracePlaybackSetter interface {
	SetPlaybackSamplesObserver(func(context.Context, int, []int16) error)
}

type traceRenderedSetter interface {
	SetRenderedSamplesObserver(func(int, []int16)) bool
}

type tracePlaybackRenderSetter interface {
	SetPlaybackRenderObserver(audio.PlaybackRenderObserver)
}

func (s publicTraceDeviceService) Open(ctx context.Context, request runtimeDevices.Request) (runtimeDevices.Handle, error) {
	if s.inner == nil {
		return nil, runtimeDevices.ErrUnavailable
	}
	handle, err := s.inner.Open(ctx, request)
	if err != nil || handle == nil || s.trace == nil {
		return handle, err
	}
	ports := handle.Media()
	capture := s.bindTraceCapture(ports.Capture, request)
	monitor := s.bindTracePlayback(ports.Playback, request, ctx)
	if capture == nil && monitor == nil {
		return handle, nil
	}
	return &publicTraceDeviceHandle{inner: handle, capture: capture, monitor: monitor}, nil
}

func (s publicTraceDeviceService) bindTraceCapture(port runtimeDevices.Capture, request runtimeDevices.Request) runtimeDevices.Capture {
	if port == nil {
		return nil
	}
	if setter, ok := port.(traceCapturePreGateSetter); ok {
		setter.SetPreGateSamplesObserver(s.trace.binding.PreGateSamplesObserver)
	}
	if setter, ok := port.(traceCaptureUploadedSetter); ok {
		setter.SetUploadedSamplesObserver(s.trace.binding.UploadedSamplesObserver)
		return nil
	}
	if s.trace.binding.UploadedSamplesObserver == nil {
		return nil
	}
	// File-backed capture has no device-owned upload setter. Wrap the exact
	// outbound handoff instead: a frame is uploaded only after the provider
	// endpoint accepts it, never from queue admission or replay bookkeeping.
	return &traceCapture{inner: port, observer: s.trace.binding.UploadedSamplesObserver, rate: request.SampleRate}
}

func (s publicTraceDeviceService) bindTracePlayback(port runtimeDevices.Playback, request runtimeDevices.Request, ctx context.Context) *remoteRenderMonitor {
	if !request.PlaybackEnabled || port == nil {
		return nil
	}
	if setter, ok := port.(tracePlaybackSetter); ok {
		setter.SetPlaybackSamplesObserver(s.trace.binding.PlaybackSamplesObserver)
	}
	rendered := s.bindRenderedObserver(port)
	if rendered {
		return nil
	}
	monitor := s.openRemoteRenderMonitor(ctx, request, port)
	if monitor != nil {
		monitor.Start()
		return monitor
	}
	if s.trace.binding.RenderedSamplesUnavailable != nil {
		s.trace.binding.RenderedSamplesUnavailable()
	}
	return nil
}

func (s publicTraceDeviceService) bindRenderedObserver(port runtimeDevices.Playback) bool {
	if setter, ok := port.(traceRenderedSetter); ok {
		return setter.SetRenderedSamplesObserver(s.trace.binding.RenderedSamplesObserver)
	}
	if setter, ok := port.(tracePlaybackRenderSetter); ok {
		setter.SetPlaybackRenderObserver(audio.PlaybackRenderObserver(s.trace.binding.RenderedSamplesObserver))
		return true
	}
	return false
}

func (s publicTraceDeviceService) openRemoteRenderMonitor(ctx context.Context, request runtimeDevices.Request, port runtimeDevices.Playback) *remoteRenderMonitor {
	if strings.TrimSpace(request.RemoteEndpoint) == "" {
		return nil
	}
	monitor, err := newRemoteRenderMonitor(ctx, request, port, s.trace.binding.RenderedSamplesObserver)
	if err != nil {
		return nil
	}
	return monitor
}

type publicTraceDeviceHandle struct {
	inner   runtimeDevices.Handle
	capture runtimeDevices.Capture
	monitor *remoteRenderMonitor

	closeOnce sync.Once
	closeErr  error
}

func (h *publicTraceDeviceHandle) Media() runtimeDevices.MediaPorts {
	if h == nil || h.inner == nil {
		return runtimeDevices.MediaPorts{}
	}
	ports := h.inner.Media()
	if h.capture != nil {
		ports.Capture = h.capture
	}
	return ports
}

func (h *publicTraceDeviceHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.monitor != nil {
			h.monitor.Stop()
		}
		if h.inner != nil {
			h.closeErr = h.inner.Close()
		}
	})
	return h.closeErr
}

type traceCapture struct {
	inner    runtimeDevices.Capture
	observer runtimeSessionTrace.CaptureSamplesObserver
	rate     int
}

func (c *traceCapture) Pump(ctx context.Context, outbound audio.OutboundMedia) error {
	if c == nil || c.inner == nil {
		return runtimeDevices.ErrUnavailable
	}
	if outbound == nil {
		return fmt.Errorf("%w: provider outbound media is nil", runtimeDevices.ErrInvalidRequest)
	}
	return c.inner.Pump(ctx, &traceCaptureOutbound{target: outbound, observer: c.observer, rate: c.rate})
}

func (c *traceCapture) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

type traceCaptureOutbound struct {
	target   audio.OutboundMedia
	observer runtimeSessionTrace.CaptureSamplesObserver
	rate     int
}

func (o *traceCaptureOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	if o == nil || o.target == nil {
		return fmt.Errorf("%w: provider outbound media is nil", runtimeDevices.ErrInvalidRequest)
	}
	if err := o.target.WriteFrame(ctx, frame); err != nil {
		return err
	}
	if o.observer != nil && len(frame.Samples) > 0 {
		rate := frame.Format.SampleRate
		if rate <= 0 {
			rate = o.rate
		}
		o.observer(rate, append([]int16(nil), frame.Samples...))
	}
	return nil
}

func (o *traceCaptureOutbound) Close() error {
	if o == nil || o.target == nil {
		return nil
	}
	return o.target.Close()
}

var _ runtimeDevices.Capture = (*traceCapture)(nil)
var _ audio.OutboundMedia = (*traceCaptureOutbound)(nil)

type traceDeviceSampleRate interface {
	DeviceSampleRate() int
}

// remoteRenderMonitor mirrors the cumulative PCM retained by the public
// loopback device-server snapshot. The server records samples only after its
// simulated device callback consumes the playback queue, so this is an
// invocation-scoped transport mirror of the render boundary, never a queue
// admission or provider/file fallback.
type remoteRenderMonitor struct {
	endpoint string
	rate     int
	observer runtimeSessionTrace.CaptureSamplesObserver

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu   sync.Mutex
	seen int
}

func newRemoteRenderMonitor(ctx context.Context, request runtimeDevices.Request, playback runtimeDevices.Playback, observer runtimeSessionTrace.CaptureSamplesObserver) (*remoteRenderMonitor, error) {
	if strings.TrimSpace(request.RemoteEndpoint) == "" || observer == nil {
		return nil, errors.New("remote render observer is unavailable")
	}
	probeContext := ctx
	if probeContext == nil {
		probeContext = context.Background()
	}
	probeContext, cancel := context.WithTimeout(probeContext, time.Second)
	defer cancel()
	snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(probeContext, request.RemoteEndpoint)
	if err != nil {
		return nil, err
	}
	rate := request.SampleRate
	if sampleRate, ok := playback.(traceDeviceSampleRate); ok && sampleRate.DeviceSampleRate() > 0 {
		rate = sampleRate.DeviceSampleRate()
	}
	if rate <= 0 {
		rate = audio.SampleRate
	}
	return &remoteRenderMonitor{
		endpoint: strings.TrimSpace(request.RemoteEndpoint),
		rate:     rate,
		observer: observer,
		ctx:      context.Background(),
		cancel:   func() {},
		done:     make(chan struct{}),
		seen:     len(snapshot.RenderedSamples),
	}, nil
}

func (m *remoteRenderMonitor) Start() {
	if m == nil {
		return
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	go m.run()
}

func (m *remoteRenderMonitor) run() {
	if m == nil {
		return
	}
	defer close(m.done)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.poll(m.ctx)
		}
	}
}

func (m *remoteRenderMonitor) poll(ctx context.Context) {
	if m == nil || m.observer == nil {
		return
	}
	pollContext, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(pollContext, m.endpoint)
	cancel()
	if err != nil {
		return
	}
	m.mu.Lock()
	if len(snapshot.RenderedSamples) < m.seen {
		m.mu.Unlock()
		return
	}
	if len(snapshot.RenderedSamples) == m.seen {
		m.mu.Unlock()
		return
	}
	samples := append([]int16(nil), snapshot.RenderedSamples[m.seen:]...)
	m.seen = len(snapshot.RenderedSamples)
	rate := m.rate
	m.mu.Unlock()
	if len(samples) > 0 {
		m.observer(rate, samples)
	}
}

func (m *remoteRenderMonitor) Stop() {
	if m == nil {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	select {
	case <-m.done:
	default:
		<-m.done
	}
	// The final poll happens before the owning device handle closes so samples
	// from the last callback are not lost to a fast session teardown.
	m.poll(context.Background())
}

func wrapTraceDeviceService(service runtimeDevices.Service, trace *publicTraceRun) runtimeDevices.Service {
	if service == nil || trace == nil {
		return service
	}
	return publicTraceDeviceService{inner: service, trace: trace}
}
