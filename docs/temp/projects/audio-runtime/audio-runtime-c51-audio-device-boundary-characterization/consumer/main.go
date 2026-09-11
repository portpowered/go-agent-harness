package consumer

import (
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// PCMFrame is the public media value used by this external embedding module.
type PCMFrame = audio.PCMFrame

// NewDeviceService proves that an embedding consumer can assemble the public
// runtime service without importing agent-cli, flags, terminal state, or an
// application initialization package.
func NewDeviceService(registry devicegw.DeviceRegistry) runtimeDevices.Service {
	return devicewire.NewService(registry)
}
