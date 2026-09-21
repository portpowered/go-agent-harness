// Package file owns finite file-backed media admission for the runtime device
// service. Hosts open the canonical audio source and sink before admission;
// this package only owns conversion, bounded pumping, and their lifetime.
package file

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/endpoint"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const defaultProviderSampleRate = 24000

// Factory adapts caller-opened audio ports to the public device service. It is
// stateless and safe to reuse across invocations.
type Factory struct{ audio audioio.Service }

func NewFactory(service audioio.Service) *Factory { return &Factory{audio: service} }

func (f *Factory) ValidateRemoteEndpoint(value string) error {
	return endpoint.ValidateRemoteEndpoint(value)
}

func (f *Factory) Open(ctx context.Context, request devices.Request) (devices.Handle, error) {
	if f == nil || f.audio == nil {
		return nil, fmt.Errorf("%w: audio service is unavailable", devices.ErrUnavailable)
	}
	if err := validateRequest(ctx, request); err != nil {
		return nil, err
	}
	providerRate, err := normalizeRequest(request)
	if err != nil {
		return nil, err
	}
	capture, err := f.openCapture(ctx, request, providerRate)
	if err != nil {
		return nil, err
	}
	playback, err := f.openPlayback(ctx, request, providerRate)
	if err != nil {
		return nil, errors.Join(err, closeCapture(capture))
	}
	return newHandle(capture, playback), nil
}

func (f *Factory) BindRTC(context.Context, devices.RTCBindingRequest) (devices.RTCBinding, error) {
	return nil, devices.ErrUnavailable
}

func validateRequest(ctx context.Context, request devices.Request) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if request.CaptureEnabled && request.FileInput == nil {
		return fmt.Errorf("%w: finite capture source is required", devices.ErrInvalidRequest)
	}
	if request.PlaybackEnabled && request.FileOutput == nil {
		return fmt.Errorf("%w: finite playback sink is required", devices.ErrInvalidRequest)
	}
	if !request.CaptureEnabled && !request.PlaybackEnabled {
		return fmt.Errorf("%w: at least one media direction must be enabled", devices.ErrInvalidRequest)
	}
	if request.FileInput != nil && request.FileInput.Source == nil {
		return fmt.Errorf("%w: finite capture source is nil", devices.ErrInvalidRequest)
	}
	if request.FileOutput != nil && request.FileOutput.Sink == nil {
		return fmt.Errorf("%w: finite playback sink is nil", devices.ErrInvalidRequest)
	}
	if request.SampleRate < 0 || request.Channels < 0 {
		return fmt.Errorf("%w: provider sample rate and channels must not be negative", devices.ErrInvalidRequest)
	}
	return validatePortRates(request)
}

func validatePortRates(request devices.Request) error {
	if request.FileInput != nil && request.FileInput.SampleRate < 0 {
		return fmt.Errorf("%w: finite input sample rate must not be negative", devices.ErrInvalidRequest)
	}
	if request.FileOutput != nil && request.FileOutput.SampleRate < 0 {
		return fmt.Errorf("%w: finite output sample rate must not be negative", devices.ErrInvalidRequest)
	}
	return nil
}

func normalizeRequest(request devices.Request) (int, error) {
	providerRate := request.SampleRate
	if providerRate <= 0 {
		providerRate = defaultProviderSampleRate
	}
	channels := request.Channels
	if channels == 0 {
		channels = sharedaudio.Channels
	}
	if channels != sharedaudio.Channels {
		return 0, fmt.Errorf("%w: finite media requires mono audio, got %d channels", devices.ErrInvalidRequest, channels)
	}
	return providerRate, nil
}

func (f *Factory) openCapture(ctx context.Context, request devices.Request, providerRate int) (*fileCapture, error) {
	if !request.CaptureEnabled {
		return nil, nil
	}
	return newCapture(ctx, *request.FileInput, providerRate, f.audio)
}

func (f *Factory) openPlayback(ctx context.Context, request devices.Request, providerRate int) (*filePlayback, error) {
	if !request.PlaybackEnabled {
		return nil, nil
	}
	return newPlayback(ctx, *request.FileOutput, providerRate, f.audio)
}

func newHandle(capture *fileCapture, playback *filePlayback) devices.Handle {
	var capturePort devices.Capture
	if capture != nil {
		capturePort = capture
	}
	var playbackPort devices.Playback
	if playback != nil {
		playbackPort = playback
	}
	return &handle{
		ports: devices.MediaPorts{Capture: capturePort, Playback: playbackPort},
		close: func() error { return errors.Join(closeCapture(capture), closePlayback(playback)) },
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func closeCapture(capture *fileCapture) error {
	if capture == nil {
		return nil
	}
	return capture.Close()
}

func closePlayback(playback *filePlayback) error {
	if playback == nil {
		return nil
	}
	return playback.Close()
}

type handle struct {
	ports devices.MediaPorts
	close func() error

	once     sync.Once
	closeMu  sync.Mutex
	closeErr error
}

func (h *handle) Media() devices.MediaPorts {
	if h == nil {
		return devices.MediaPorts{}
	}
	return h.ports
}

func (h *handle) Close() error {
	if h == nil {
		return nil
	}
	h.once.Do(func() {
		if h.close != nil {
			h.closeMu.Lock()
			h.closeErr = h.close()
			h.closeMu.Unlock()
		}
	})
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	return h.closeErr
}

var _ devices.Service = (*Factory)(nil)
var _ devices.Handle = (*handle)(nil)
