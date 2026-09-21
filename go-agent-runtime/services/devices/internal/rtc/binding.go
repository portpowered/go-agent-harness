// Package rtc owns the complete local-device to provider-session lifecycle.
// It is intentionally private: callers receive only devices.RTCBinding.
package rtc

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const inputFlag = "audio-in-device"
const outputFlag = "audio-out-device"

type Factory struct{ registry devicegw.DeviceRegistry }

func NewFactory(registry devicegw.DeviceRegistry) *Factory { return &Factory{registry: registry} }

func normalizeSelector(id string) devicegw.DeviceID {
	if strings.EqualFold(strings.TrimSpace(id), "default") {
		return ""
	}
	return devicegw.DeviceID(strings.TrimSpace(id))
}

type bindingError struct {
	flag      string
	direction devicegw.Direction
	id        string
	err       error
}

func (e *bindingError) Error() string {
	return fmt.Sprintf("%s could not select %s audio device %q: %v", e.flag, e.direction, e.id, e.err)
}
func (e *bindingError) Unwrap() error { return e.err }

type binding struct {
	source     *devicert.RTCDeviceSource
	sink       *devicert.RTCDeviceSink
	capture    *devicert.BufferedCapture
	feedback   *audio.PCM16FeedbackGate
	inferencer *inferencer
	once       sync.Once
	err        error
}

func (b *binding) Inferencer() messages.SessionInferencer {
	if b == nil {
		return nil
	}
	return b.inferencer
}
func (b *binding) Errors() <-chan error {
	if b == nil || b.inferencer == nil {
		return nil
	}
	return b.inferencer.errors
}
func (b *binding) AudioPorts() *audiosubsystem.Ports {
	if b == nil || b.capture == nil && b.sink == nil {
		return nil
	}
	ports := &audiosubsystem.Ports{}
	if b.capture != nil {
		ports.Capture = b.capture.Control()
	}
	if b.sink != nil {
		ports.Playback = b.sink.PlaybackBuffer()
		ports.Commands = b.sink.PlaybackCommands()
	}
	return ports
}
func (b *binding) SelectedDeviceIDs() (input, output string) {
	if b == nil {
		return "", ""
	}
	if b.source != nil {
		input = string(b.source.DeviceID())
	}
	if b.sink != nil {
		output = string(b.sink.DeviceID())
	}
	return input, output
}
func (b *binding) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() { b.err = errors.Join(closeSink(b.sink), closeSource(b.source), closeFeedback(b.feedback)) })
	return b.err
}
func closeSink(s *devicert.RTCDeviceSink) error {
	if s == nil {
		return nil
	}
	return s.Close()
}
func closeSource(s *devicert.RTCDeviceSource) error {
	if s == nil {
		return nil
	}
	return s.Close()
}
func closeFeedback(f *audio.PCM16FeedbackGate) error {
	if f == nil {
		return nil
	}
	return f.Close()
}

type mediaErrorCode string

func (e mediaErrorCode) Error() string { return string(e) }

const ErrSessionMediaUnavailable mediaErrorCode = "RTC session media endpoints are unavailable"

var _ devices.RTCBinding = (*binding)(nil)
