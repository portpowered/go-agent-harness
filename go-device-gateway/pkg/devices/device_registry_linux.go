//go:build linux && cgo && !nomicrophone

package devices

// NewHostDeviceRegistry returns the registry for the current Linux host.
func NewHostDeviceRegistry() DeviceRegistry { return NewDeviceRegistry() }
