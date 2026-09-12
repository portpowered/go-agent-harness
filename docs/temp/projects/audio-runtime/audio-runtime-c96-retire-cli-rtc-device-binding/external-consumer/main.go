package consumer

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	devicebindingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding/wire"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// OpenVirtual proves that a host can compose the service without importing
// its private implementation or any CLI package.
func OpenVirtual(registry devicegw.DeviceRegistry) (*devicebinding.Binding, error) {
	return devicebindingwire.NewService().Open(devicebinding.Request{
		Registry: registry, InputDevice: "default", OutputDevice: "default",
		InputPresent: true, OutputPresent: true, BypassSelfHearing: true,
	})
}
