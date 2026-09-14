package agentruntime

import runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"

func sessionInputDeviceSelected(request runtimedevices.RTCBindingRequest) bool {
	return request.InputPresent || request.InputDevice != ""
}

func sessionOutputDeviceSelected(request runtimedevices.RTCBindingRequest) bool {
	return request.OutputPresent || request.OutputDevice != ""
}

func sessionDevicesSelected(request runtimedevices.RTCBindingRequest) bool {
	return sessionInputDeviceSelected(request) || sessionOutputDeviceSelected(request)
}
