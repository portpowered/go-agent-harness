//go:build windows && !nomicrophone

package audio

// NewPlatformDeviceRegistry returns the host's lazy WASAPI registry.
// COM and native handles are acquired only when the caller invokes a
// registry operation.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewWASAPIDeviceRegistry() }
