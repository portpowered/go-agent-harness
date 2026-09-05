package devices

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
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

// DeviceProbeInputPlan is the authored microphone contract for a device-tier
// scenario. It carries presentation-safe text and corpus identity only.
type DeviceProbeInputPlan struct {
	CorpusID  string
	Utterance string
}

// DeviceProbeRequest carries runtime configuration for one physical device
// probe. It contains no registry or opened-device handle; the implementation
// owns those resources behind the service boundary.
type DeviceProbeRequest struct {
	Scenario             probe.Scenario
	Provider             string
	Model                string
	APIKey               string
	BaseURL              string
	ConfigDir            string
	CaptureTime          time.Duration
	SessionInferencer    messages.SessionInferencer
	Instructions         string
	InstructionsObserved func(string)
	WebSocketDialer      transport.Dialer
}

// DeviceProbeService runs a selected device-tier scenario behind the private
// device implementation. Selection and all device leases remain service-owned.
type DeviceProbeService interface {
	Run(context.Context, DeviceProbeRequest) (probe.ObservationSnapshot, error)
}

// DefaultDeviceProbeCaptureDuration is the default microphone capture window
// used by the device probe transport when a request omits one.
const DefaultDeviceProbeCaptureDuration = 5 * time.Second

// ProbeInputPlan validates and returns the authored input contract for a
// scenario without opening a device.
func ProbeInputPlan(scenario probe.Scenario) (DeviceProbeInputPlan, error) {
	var corpusID string
	count := 0
	var utterance string
	for _, step := range scenario.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		if kind != probe.StepSendAudio {
			continue
		}
		count++
		candidate := step.CorpusID
		if candidate == "" {
			candidate = step.Corpus.CorpusID
		}
		if corpusID == "" {
			corpusID = candidate
		}
		if corpusID != candidate {
			return DeviceProbeInputPlan{}, fmt.Errorf("device probe scenario must contain exactly one send_audio step with one committed audio corpus")
		}
		utterance = strings.TrimSpace(step.Text)
	}
	if count != 1 || strings.TrimSpace(corpusID) == "" {
		return DeviceProbeInputPlan{}, fmt.Errorf("device probe scenario must contain exactly one send_audio step with a committed audio corpus")
	}
	if utterance == "" {
		return DeviceProbeInputPlan{}, fmt.Errorf("device probe send_audio step for corpus %q must declare text for the manual microphone utterance", corpusID)
	}
	return DeviceProbeInputPlan{CorpusID: corpusID, Utterance: utterance}, nil
}

// ProbeRMS computes the capture energy used by device-tier assertions.
func ProbeRMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}
