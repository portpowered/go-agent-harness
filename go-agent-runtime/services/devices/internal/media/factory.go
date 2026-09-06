// Package media adapts the device gateway's bounded PCM workers to the public
// device service. Room orchestration never opens a registry directly.
package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

// Factory owns the host device registry while exposing only the gateway-owned
// buffered source and sink workers to the room lifecycle.
type Factory struct {
	registry devicegw.DeviceRegistry
	format   mixer.Format
}

// NewFactory creates the device-backed service. The configured format is used
// when a request omits one; local conversion remains in the gateway-owned RTC
// workers.
func NewFactory(registry devicegw.DeviceRegistry, format mixer.Format) *Factory {
	defaults := mixer.DefaultFormat()
	if format.SampleRate <= 0 {
		format.SampleRate = defaults.SampleRate
	}
	if format.Channels <= 0 {
		format.Channels = defaults.Channels
	}
	if format.FrameDuration <= 0 {
		format.FrameDuration = defaults.FrameDuration
	}
	return &Factory{registry: registry, format: format}
}

// Open admits the input and output workers as one lifecycle unit. If output
// admission fails after capture succeeds, capture is closed before the error
// is returned so partial startup cannot leak a device handle.
func (f *Factory) Open(ctx context.Context, request devices.Request) (devices.Handle, error) {
	if err := f.validateRequest(ctx, request); err != nil {
		return nil, err
	}
	rate, err := f.requestFormat(request.SampleRate, request.Channels)
	if err != nil {
		return nil, err
	}
	input, err := f.openInput(request, rate)
	if err != nil {
		return nil, err
	}
	output, err := f.openOutput(request, rate)
	if err != nil {
		return nil, errors.Join(err, closeInput(input))
	}
	return newHandle(input, output), nil
}

func (f *Factory) validateRequest(ctx context.Context, request devices.Request) error {
	if f == nil || f.registry == nil {
		return errors.Join(devices.ErrUnavailable, devicegw.ErrNilDeviceRegistry)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if request.CaptureEnabled || request.PlaybackEnabled {
		return nil
	}
	return fmt.Errorf("%w: at least one media direction must be enabled", devices.ErrInvalidRequest)
}

func (f *Factory) openInput(request devices.Request, rate int) (*devicert.RTCDeviceSource, error) {
	if !request.CaptureEnabled {
		return nil, nil
	}
	input, err := devicert.NewRTCDeviceSourceAtRate(f.registry, devicegw.DeviceID(strings.TrimSpace(request.InputDevice)), rate)
	if err != nil {
		return nil, fmt.Errorf("open input device %q: %w", request.InputDevice, err)
	}
	return input, nil
}

func (f *Factory) openOutput(request devices.Request, rate int) (*devicert.RTCDeviceSink, error) {
	if !request.PlaybackEnabled {
		return nil, nil
	}
	output, err := devicert.NewRTCDeviceSinkAtRateWithOptions(
		f.registry, devicegw.DeviceID(strings.TrimSpace(request.OutputDevice)), rate,
		request.PlaybackProfile, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("open output device %q: %w", request.OutputDevice, err)
	}
	return output, nil
}

func closeInput(input *devicert.RTCDeviceSource) error {
	if input == nil {
		return nil
	}
	return input.Close()
}

func newHandle(input *devicert.RTCDeviceSource, output *devicert.RTCDeviceSink) devices.Handle {
	ports := devices.MediaPorts{}
	if input != nil {
		ports.Capture = input
	}
	if output != nil {
		ports.Playback = output
	}
	return &handle{ports: ports, close: func() error { return errors.Join(closeInput(input), closeOutput(output)) }}
}

func closeOutput(output *devicert.RTCDeviceSink) error {
	if output == nil {
		return nil
	}
	return output.Close()
}

func (f *Factory) requestFormat(sampleRate, channels int) (int, error) {
	if sampleRate < 0 {
		return 0, fmt.Errorf("%w: sample rate must not be negative", devices.ErrInvalidRequest)
	}
	if channels < 0 {
		return 0, fmt.Errorf("%w: channel count must not be negative", devices.ErrInvalidRequest)
	}
	if sampleRate == 0 {
		sampleRate = f.format.SampleRate
	}
	if channels == 0 {
		channels = f.format.Channels
	}
	if channels != 1 {
		return 0, fmt.Errorf("%w: device media requires mono format, got %d channels", devices.ErrInvalidRequest, channels)
	}
	if sampleRate <= 0 {
		return 0, fmt.Errorf("%w: device media requires a positive sample rate", devices.ErrInvalidRequest)
	}
	return sampleRate, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
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
