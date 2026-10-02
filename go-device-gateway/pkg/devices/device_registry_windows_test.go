//go:build windows && !nomicrophone

package devices

import "testing"

// TestWASAPIHostRegistrySelection covers host registry selection for a
// Windows build with the microphone backend: the host registry is WASAPI.
func TestWASAPIHostRegistrySelection(t *testing.T) {
	if registry, ok := NewHostDeviceRegistry().(*wasapiDeviceRegistry); !ok || registry == nil {
		t.Fatalf("NewHostDeviceRegistry() = %T, want *wasapiDeviceRegistry", NewHostDeviceRegistry())
	}
}
