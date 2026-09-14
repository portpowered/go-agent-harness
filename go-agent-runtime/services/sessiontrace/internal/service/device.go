package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type traceDeviceService struct {
	inner   runtimeDevices.Service
	binding sessiontrace.DeviceBinding
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

func wrapDeviceService(service runtimeDevices.Service, binding sessiontrace.DeviceBinding) runtimeDevices.Service {
	if service == nil {
		return nil
	}
	return traceDeviceService{inner: service, binding: binding}
}

func (s traceDeviceService) Open(ctx context.Context, request runtimeDevices.Request) (runtimeDevices.Handle, error) {
	if s.inner == nil {
		return nil, runtimeDevices.ErrUnavailable
	}
	handle, err := s.inner.Open(ctx, request)
	if err != nil || handle == nil {
		return handle, err
	}
	ports := handle.Media()
	capture := s.bindTraceCapture(ports.Capture, request)
	monitor := s.bindTracePlayback(ports.Playback, request, ctx)
	if capture == nil && monitor == nil {
		return handle, nil
	}
	return &traceDeviceHandle{inner: handle, capture: capture, monitor: monitor}, nil
}

func (s traceDeviceService) bindTraceCapture(port runtimeDevices.Capture, request runtimeDevices.Request) runtimeDevices.Capture {
	if port == nil {
		return nil
	}
	if setter, ok := port.(traceCapturePreGateSetter); ok {
		setter.SetPreGateSamplesObserver(s.binding.PreGateSamplesObserver)
	}
	if setter, ok := port.(traceCaptureUploadedSetter); ok {
		setter.SetUploadedSamplesObserver(s.binding.UploadedSamplesObserver)
		return nil
	}
	if s.binding.UploadedSamplesObserver == nil {
		return nil
	}
	return &traceCapture{inner: port, observer: s.binding.UploadedSamplesObserver, rate: request.SampleRate}
}

func (s traceDeviceService) bindTracePlayback(port runtimeDevices.Playback, request runtimeDevices.Request, ctx context.Context) *remoteRenderMonitor {
	if !request.PlaybackEnabled || port == nil {
		return nil
	}
	if setter, ok := port.(tracePlaybackSetter); ok {
		setter.SetPlaybackSamplesObserver(s.binding.PlaybackSamplesObserver)
	}
	if s.bindRenderedObserver(port) {
		return nil
	}
	monitor := s.openRemoteRenderMonitor(ctx, request, port)
	if monitor != nil {
		monitor.Start()
		return monitor
	}
	if s.binding.RenderedSamplesUnavailable != nil {
		s.binding.RenderedSamplesUnavailable()
	}
	return nil
}

func (s traceDeviceService) bindRenderedObserver(port runtimeDevices.Playback) bool {
	if setter, ok := port.(traceRenderedSetter); ok {
		return setter.SetRenderedSamplesObserver(s.binding.RenderedSamplesObserver)
	}
	if setter, ok := port.(tracePlaybackRenderSetter); ok {
		setter.SetPlaybackRenderObserver(audio.PlaybackRenderObserver(s.binding.RenderedSamplesObserver))
		return true
	}
	return false
}

func (s traceDeviceService) openRemoteRenderMonitor(ctx context.Context, request runtimeDevices.Request, port runtimeDevices.Playback) *remoteRenderMonitor {
	if strings.TrimSpace(request.RemoteEndpoint) == "" {
		return nil
	}
	monitor, err := newRemoteRenderMonitor(ctx, request, port, s.binding.RenderedSamplesObserver)
	if err != nil {
		return nil
	}
	return monitor
}

type traceDeviceHandle struct {
	inner   runtimeDevices.Handle
	capture runtimeDevices.Capture
	monitor *remoteRenderMonitor

	closeOnce sync.Once
	closeErr  error
}

func (h *traceDeviceHandle) Media() runtimeDevices.MediaPorts {
	if h == nil || h.inner == nil {
		return runtimeDevices.MediaPorts{}
	}
	ports := h.inner.Media()
	if h.capture != nil {
		ports.Capture = h.capture
	}
	return ports
}

func (h *traceDeviceHandle) Close() error {
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
	observer sessiontrace.CaptureSamplesObserver
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
	observer sessiontrace.CaptureSamplesObserver
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

// remoteRenderMonitor mirrors the samples retained by a remote device server
// after its simulated device callback consumes the playback queue. It is an
// invocation-scoped service detail, not a provider or queue-admission tap.
type remoteRenderMonitor struct {
	endpoint string
	rate     int
	observer sessiontrace.CaptureSamplesObserver

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu   sync.Mutex
	seen int
}

const (
	remoteRenderPollInterval = 10 * time.Millisecond
	remoteRenderPollTimeout  = 100 * time.Millisecond
	remoteRenderStopTimeout  = 250 * time.Millisecond
)

func newRemoteRenderMonitor(ctx context.Context, request runtimeDevices.Request, playback runtimeDevices.Playback, observer sessiontrace.CaptureSamplesObserver) (*remoteRenderMonitor, error) {
	if strings.TrimSpace(request.RemoteEndpoint) == "" || observer == nil {
		return nil, errors.New("remote render observer is unavailable")
	}
	probeContext, cancel := context.WithTimeout(remoteRenderProbeContext(ctx), time.Second)
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

func remoteRenderProbeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
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
	ticker := time.NewTicker(remoteRenderPollInterval)
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
	pollContext, cancel := context.WithTimeout(ctx, remoteRenderPollTimeout)
	snapshot, err := devicegw.ReadRemoteDeviceServerSnapshot(pollContext, m.endpoint)
	cancel()
	if err != nil {
		return
	}
	m.mu.Lock()
	if len(snapshot.RenderedSamples) <= m.seen {
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
	timer := time.NewTimer(remoteRenderStopTimeout)
	defer timer.Stop()
	select {
	case <-m.done:
	case <-timer.C:
	}
	// Capture the final callback before the owning device handle closes. The
	// poll itself is bounded, so teardown remains bounded even if the remote
	// endpoint is unavailable.
	m.poll(context.Background())
}
