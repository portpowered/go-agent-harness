//go:build (darwin || linux) && cgo && !nomicrophone

package devices

import "fmt"

// constantDevice builds a device descriptor from test-constant identifiers.
// Invalid constants are a fixture bug, not a runtime state.
func constantDevice(backend, nativeID, name string, direction Direction) Device {
	device, err := NewDevice(backend, nativeID, name, direction)
	if err != nil {
		panic(fmt.Sprintf("devices: invalid constant device %s/%s: %v", backend, nativeID, err))
	}
	return device
}
