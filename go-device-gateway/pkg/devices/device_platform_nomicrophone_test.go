//go:build nomicrophone

package devices

import (
	"errors"
	"testing"
)

func TestPlatformDeviceRegistryNomicrophoneFallback(t *testing.T) {
	assertNomicrophoneFallback(t, "platform", NewPlatformDeviceRegistry())
	// Keep the host constructor covered as well: on Windows this build tag
	// must not select the WASAPI registry.
	assertNomicrophoneFallback(t, "host", NewHostDeviceRegistry())
}

func assertNomicrophoneFallback(t *testing.T, name string, registry DeviceRegistry) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Helper()
		if registry == nil {
			t.Fatal("NewPlatformDeviceRegistry() returned nil")
		}
		devices, err := registry.List()
		if err != nil {
			t.Fatalf("fallback List() = %v", err)
		}
		if len(devices) != 0 {
			t.Fatalf("fallback List() = %#v, want no devices", devices)
		}
		if _, err := registry.Default(DirectionInput); !errors.Is(err, ErrNoDefaultDevice) {
			t.Fatalf("fallback Default(input) = %v, want ErrNoDefaultDevice", err)
		}
		if _, err := registry.Open("virtual:missing"); !errors.Is(err, ErrDeviceNotFound) {
			t.Fatalf("fallback Open() = %v, want ErrDeviceNotFound", err)
		}
	})
}
