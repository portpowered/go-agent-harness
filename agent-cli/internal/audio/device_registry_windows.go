//go:build windows && !nomicrophone

package audio

// NewHostDeviceRegistry returns the registry for the current Windows host.
func NewHostDeviceRegistry() DeviceRegistry { return NewWASAPIDeviceRegistry() }
