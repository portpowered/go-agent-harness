package consumer

import (
	"testing"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestOpenVirtualUsesPublicServiceBoundary(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("NewVirtualRegistry() error = %v", err)
	}
	binding, err := OpenVirtual(registry)
	if err != nil {
		t.Fatalf("OpenVirtual() error = %v", err)
	}
	if binding == nil || binding.Source == nil || binding.Sink == nil {
		t.Fatalf("OpenVirtual() returned incomplete binding: %#v", binding)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
