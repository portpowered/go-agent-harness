//go:build windows && !nomicrophone

package devices

// NewPlatformDeviceRegistry returns the host's lazy WASAPI registry.
// COM and native handles are acquired only when the caller invokes a
// registry operation.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewWASAPIDeviceRegistry() }
