//go:build darwin && cgo && !nomicrophone

package devices

// NewHostDeviceRegistry returns the registry for the current macOS host.
func NewHostDeviceRegistry() DeviceRegistry { return NewCoreAudioDeviceRegistry() }
