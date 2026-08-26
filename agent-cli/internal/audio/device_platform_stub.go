//go:build (!linux && !darwin && !windows) || (linux && (!cgo || nomicrophone)) || (darwin && (!cgo || nomicrophone)) || (windows && nomicrophone)

package audio

// unavailableDeviceRegistry keeps the CLI graph constructible on unsupported
// hosts and hermetic builds. It deliberately has no devices, matching the
// typed failure behavior of a platform registry with no default endpoint.
type unavailableDeviceRegistry struct{}

func (unavailableDeviceRegistry) List() ([]Device, error) { return nil, nil }

func (unavailableDeviceRegistry) Default(direction Direction) (Device, error) {
	return Device{}, NewNoDefaultDeviceError(direction)
}

func (unavailableDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	return nil, NewDeviceNotFoundError(id)
}

// NewPlatformDeviceRegistry returns the no-device registry used when this
// build does not include a supported native audio backend.
func NewPlatformDeviceRegistry() DeviceRegistry { return unavailableDeviceRegistry{} }
