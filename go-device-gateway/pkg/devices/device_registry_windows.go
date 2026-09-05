//go:build windows && !nomicrophone

package devices

// NewHostDeviceRegistry returns the registry for the current Windows host.
func NewHostDeviceRegistry() DeviceRegistry { return NewWASAPIDeviceRegistry() }
