//go:build linux && cgo && !nomicrophone

package audio

// NewPlatformDeviceRegistry returns the host's lazy audio-device registry.
// Device enumeration and native handles are acquired only when the caller
// invokes a registry operation.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewDeviceRegistry() }
