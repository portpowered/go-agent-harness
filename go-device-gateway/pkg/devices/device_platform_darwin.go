//go:build darwin && cgo && !nomicrophone

package devices

// NewPlatformDeviceRegistry returns the host's lazy CoreAudio registry.
// Device enumeration and native handles are acquired only when the caller
// invokes a registry operation.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewCoreAudioDeviceRegistry() }
