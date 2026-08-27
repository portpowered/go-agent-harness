//go:build (!linux && !darwin && !windows) || (linux && (!cgo || nomicrophone)) || (darwin && (!cgo || nomicrophone)) || (windows && nomicrophone)

package audio

// NewPlatformDeviceRegistry returns the no-device registry used when this
// build does not include a supported native audio backend.
func NewPlatformDeviceRegistry() DeviceRegistry { return NewHostDeviceRegistry() }
