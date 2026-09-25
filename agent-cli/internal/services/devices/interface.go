package devices

import (
	"context"
	"time"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
)

// DeviceDirection is the transport-neutral spelling of a device direction.
type DeviceDirection string

const (
	DeviceDirectionInput  DeviceDirection = "input"
	DeviceDirectionOutput DeviceDirection = "output"
	DirectionInput                        = DeviceDirectionInput
	DirectionOutput                       = DeviceDirectionOutput
)

// Direction is a concise compatibility alias for callers that use the
// gateway vocabulary without importing the gateway package.
type Direction = DeviceDirection

// Device describes the stable, presentation-safe part of an audio device.
// It contains no opened device, callback, registry, or backend object.
type Device struct {
	ID          string          `json:"id"`
	Backend     string          `json:"backend,omitempty"`
	NativeID    string          `json:"native_id,omitempty"`
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name,omitempty"`
	Direction   DeviceDirection `json:"direction"`
	Default     bool            `json:"default,omitempty"`
}

// DeviceList is the result of side-effect-free enumeration.
type DeviceList struct {
	Devices []Device `json:"devices"`
}

// DeviceSelectionRequest carries CLI selectors without exposing gateway
// request types to the service boundary.
type DeviceSelectionRequest struct {
	InputSelector     string `json:"input_selector,omitempty"`
	OutputSelector    string `json:"output_selector,omitempty"`
	AudioInFile       string `json:"audio_in_file,omitempty"`
	AudioInConfigured bool   `json:"audio_in_configured,omitempty"`
	OnDeviceLoss      string `json:"on_device_loss,omitempty"`
}

// DeviceSelection contains resolved metadata. Opened gateway handles never
// cross this public service contract.
type DeviceSelection struct {
	Input          Device `json:"input"`
	Output         Device `json:"output"`
	InputSelected  bool   `json:"input_selected"`
	OutputSelected bool   `json:"output_selected"`
	LossPolicy     string `json:"loss_policy"`
}

type DeviceProbeStatus string

const (
	DeviceProbeStatusReady DeviceProbeStatus = "ready"
	DeviceProbeStatusSkip  DeviceProbeStatus = "skip"
)

type DeviceProbeSkipCode string

const (
	DeviceProbeSkipNoInputDevice  DeviceProbeSkipCode = "no_audio_input_device"
	DeviceProbeSkipNoOutputDevice DeviceProbeSkipCode = "no_audio_output_device"
	DeviceProbeSkipNoDevices      DeviceProbeSkipCode = "no_audio_input_or_output_devices"
)

// DeviceProbeAvailability is a side-effect-free enumeration result. Device
// metadata is a value snapshot; no opened gateway object crosses this edge.
type DeviceProbeAvailability struct {
	Status            DeviceProbeStatus   `json:"status"`
	ReasonCode        DeviceProbeSkipCode `json:"reason_code,omitempty"`
	Reason            string              `json:"reason,omitempty"`
	InputDeviceCount  int                 `json:"input_device_count"`
	OutputDeviceCount int                 `json:"output_device_count"`
	Devices           []Device            `json:"devices,omitempty"`
	InputDevices      []Device            `json:"input_devices,omitempty"`
	OutputDevices     []Device            `json:"output_devices,omitempty"`
}

// DeviceService is the narrow use-case contract consumed by CLI transports.
type DeviceService interface {
	Enumerate(context.Context) (DeviceList, error)
	Select(context.Context, DeviceSelectionRequest) (DeviceSelection, error)
	ProbeAvailability(context.Context) (DeviceProbeAvailability, error)
}

// DeviceProbeRequest carries runtime configuration for one physical device
// probe. The reusable runtime owns media execution and registry access.
type DeviceProbeRequest = runtimeDevices.ProbeRequest

// DeviceProbeSessionFactory constructs the provider session used by a live
// probe through the application composition graph.
type DeviceProbeSessionFactory = runtimeDevices.ProbeSessionFactory

// DeviceProbeService runs a selected device-tier scenario behind the runtime
// device implementation. Selection and all device leases remain service-owned.
type DeviceProbeService = runtimeDevices.ProbeService

// DefaultDeviceProbeCaptureDuration is the default microphone capture window
// used by the device probe transport when a request omits one.
const DefaultDeviceProbeCaptureDuration = 5 * time.Second
