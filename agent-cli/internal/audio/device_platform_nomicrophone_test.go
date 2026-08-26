//go:build nomicrophone

package audio

import (
	"errors"
	"testing"
)

func TestPlatformDeviceRegistryNomicrophoneFallback(t *testing.T) {
	registry := NewPlatformDeviceRegistry()
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
}
