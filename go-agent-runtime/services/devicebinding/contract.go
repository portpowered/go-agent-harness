// Package devicebinding owns directional RTC device admission for reusable
// runtime hosts. Registry resolution, media workers, and feedback composition
// stay behind the service boundary; callers receive only the admitted media
// endpoints and their lifecycle handle.
package devicebinding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	selfhearing "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/selfhearing"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

// Request separates command presence from opaque device IDs. An empty ID
// selects the registry default only when its direction is present.
type Request struct {
	Registry      devicegw.DeviceRegistry
	InputDevice   devicegw.DeviceID
	OutputDevice  devicegw.DeviceID
	InputPresent  bool
	OutputPresent bool

	SelfHearingConfig     selfhearing.PCM16SelfHearingConfig
	FeedbackWarningWriter io.Writer
	BypassSelfHearing     bool
	OutputSampleRate      int
	OutputVoice           string
	HoldToneConfig        *audio.HoldToneConfig
	InputSampleRate       int

	PlaybackObserver           devicert.RTCDevicePlaybackObserver
	PlaybackReceiptObserver    devicert.RTCDevicePlaybackReceiptObserver
	PlaybackSamplesObserver    devicert.RTCDevicePlaybackSamplesObserver
	PreGateSamplesObserver     devicert.RTCDeviceCaptureSamplesObserver
	UploadedSamplesObserver    devicert.RTCDeviceCaptureSamplesObserver
	RenderedSamplesObserver    devicert.RTCDeviceRenderedSamplesObserver
	RenderedSamplesUnavailable func()
	CaptureObserver            devicert.RTCDeviceCaptureObserver
	Observability              observability.Dependencies
}

// Source is the host-neutral capture capability returned by a successful
// admission. The implementation may resample between the device and provider
// rates, but exposes both negotiated values for trace owners.
type Source interface {
	DeviceID() devicegw.DeviceID
	SourceSampleRate() int
	ProviderSampleRate() int
	Pump(context.Context, audio.OutboundMedia) error
	Close() error
}

// Sink is the host-neutral playback capability returned by a successful
// admission.
type Sink interface {
	DeviceID() devicegw.DeviceID
	SampleRate() int
	ProviderSampleRate() int
	Pump(context.Context, audio.InboundMedia) error
	Close() error
}

// Capture is the bounded handoff between an admitted Source and its provider.
type Capture interface {
	Producer() audio.FrameProducer
	Consumer() audio.FrameConsumer
	Control() audio.BufferControl
}

// Feedback is the read-only lifecycle capability of a paired-device feedback
// controller. The service constructs and attaches its concrete implementation.
type Feedback interface {
	CapturePosition() time.Duration
	Close() error
}

// InputSelected reports whether capture was explicitly requested.
func (r Request) InputSelected() bool { return r.InputPresent || r.InputDevice != "" }

// OutputSelected reports whether playback was explicitly requested.
func (r Request) OutputSelected() bool { return r.OutputPresent || r.OutputDevice != "" }

// Selected reports whether either directional endpoint is requested.
func (r Request) Selected() bool { return r.InputSelected() || r.OutputSelected() }

// Binding owns the selected endpoints and the optional feedback gate. The
// feedback field is exposed as a diagnostic capability for legacy hosts; the
// service remains responsible for constructing and wiring it.
type Binding struct {
	Source   Source
	Sink     Sink
	Capture  Capture
	Feedback Feedback

	closeOnce sync.Once
	closeErr  error
}

// Close stops playback before capture and then releases the feedback gate.
// It is idempotent and preserves every cleanup error.
func (b *Binding) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		var sourceErr, sinkErr error
		if b.Sink != nil {
			sinkErr = b.Sink.Close()
		}
		if b.Source != nil {
			sourceErr = b.Source.Close()
		}
		var feedbackErr error
		if b.Feedback != nil {
			feedbackErr = b.Feedback.Close()
		}
		b.closeErr = errors.Join(sourceErr, sinkErr, feedbackErr)
	})
	return b.closeErr
}

// Service admits the local endpoints for one invocation.
type Service interface {
	Open(Request) (*Binding, error)
}

// BindingError identifies the directional selector that failed while keeping
// the registry's typed error available through errors.Is/errors.As.
type BindingError struct {
	Flag      string
	Direction devicegw.Direction
	DeviceID  devicegw.DeviceID
	Err       error
}

func (e *BindingError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s could not select %s audio device %q", e.Flag, e.Direction, e.DeviceID)
	}
	return fmt.Sprintf("%s could not select %s audio device %q: %v", e.Flag, e.Direction, e.DeviceID, e.Err)
}

func (e *BindingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
