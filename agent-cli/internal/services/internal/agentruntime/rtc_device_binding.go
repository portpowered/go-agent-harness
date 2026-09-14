package agentruntime

import (
	devicecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	devicebindingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const (
	SessionAudioInDeviceFlag  = devicecontract.SessionAudioInDeviceFlag
	SessionAudioOutDeviceFlag = devicecontract.SessionAudioOutDeviceFlag
)

var ErrSessionAudioOutputConflict = devicecontract.ErrSessionAudioOutputConflict

type SessionAudioDeviceConflictError = devicecontract.SessionAudioDeviceConflictError
type RTCDeviceBindingError = devicebinding.BindingError

// RTCDeviceBindingRequest is a deprecated compatibility DTO; use devicebinding.Request.
type RTCDeviceBindingRequest devicebinding.Request

func (r RTCDeviceBindingRequest) serviceRequest() devicebinding.Request {
	return devicebinding.Request(r)
}
func (r RTCDeviceBindingRequest) inputSelected() bool  { return r.serviceRequest().InputSelected() }
func (r RTCDeviceBindingRequest) outputSelected() bool { return r.serviceRequest().OutputSelected() }

// RTCDeviceBinding is a deprecated concrete compatibility adapter; use devicebinding.Binding.
type RTCDeviceBinding struct {
	Source   *devicert.RTCDeviceSource
	Sink     *devicert.RTCDeviceSink
	Capture  *devicert.BufferedCapture
	feedback *audio.PCM16FeedbackGate
	inner    *devicebinding.Binding
}

func adaptRTCDeviceBinding(inner *devicebinding.Binding) *RTCDeviceBinding {
	if inner == nil {
		return nil
	}
	return &RTCDeviceBinding{
		Source:   as[devicert.RTCDeviceSource](inner.Source),
		Sink:     as[devicert.RTCDeviceSink](inner.Sink),
		Capture:  as[devicert.BufferedCapture](inner.Capture),
		feedback: as[audio.PCM16FeedbackGate](inner.Feedback),
		inner:    inner,
	}
}
func as[T any](value any) *T {
	result, ok := value.(*T)
	if !ok {
		return nil
	}
	return result
}
func (b *RTCDeviceBinding) Close() error {
	if b == nil {
		return nil
	}
	if b.inner != nil {
		return b.inner.Close()
	}
	return (&devicebinding.Binding{Source: b.Source, Sink: b.Sink, Feedback: b.feedback}).Close()
}

// PrepareRTCDeviceBindings is a deprecated compatibility entry point; use
// devicebindingwire.NewService().Open.
func PrepareRTCDeviceBindings(request RTCDeviceBindingRequest) (*RTCDeviceBinding, error) {
	binding, err := devicebindingwire.NewService().Open(request.serviceRequest())
	if err != nil {
		return nil, err
	}
	return adaptRTCDeviceBinding(binding), nil
}
func OpenRTCDeviceBindings(request RTCDeviceBindingRequest) (*RTCDeviceBinding, error) {
	return PrepareRTCDeviceBindings(request)
}
func NewRTCDeviceBinding(request RTCDeviceBindingRequest) (*RTCDeviceBinding, error) {
	return PrepareRTCDeviceBindings(request)
}
