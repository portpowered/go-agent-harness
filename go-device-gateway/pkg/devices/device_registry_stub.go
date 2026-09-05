//go:build (!linux && !darwin && !windows) || ((linux || darwin) && (!cgo || nomicrophone)) || (windows && nomicrophone)

package devices

// unavailableDeviceRegistry is the no-CGO/platform fallback. It deliberately
// reports an empty snapshot so device-tier callers can emit their structured
// SKIP result without treating a host that cannot expose audio as an error.
type unavailableDeviceRegistry struct{}

// NewHostDeviceRegistry returns an empty registry when the host backend is not
// available in the current build.
func NewHostDeviceRegistry() DeviceRegistry { return unavailableDeviceRegistry{} }

func (unavailableDeviceRegistry) List() ([]Device, error) { return nil, nil }

func (unavailableDeviceRegistry) Default(direction Direction) (Device, error) {
	return Device{}, NewNoDefaultDeviceError(direction)
}

func (unavailableDeviceRegistry) Open(id DeviceID) (OpenedDevice, error) {
	return nil, NewDeviceNotFoundError(id)
}
