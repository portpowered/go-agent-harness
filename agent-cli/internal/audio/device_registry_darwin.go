//go:build darwin && cgo && !nomicrophone

package audio

// NewHostDeviceRegistry returns the registry for the current macOS host.
func NewHostDeviceRegistry() DeviceRegistry { return NewCoreAudioDeviceRegistry() }
