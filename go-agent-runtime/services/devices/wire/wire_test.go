package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestPublicRTCBindingOwnsCloseableDeviceLifecycle(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("NewVirtualRegistry() error = %v", err)
	}
	service := NewService(registry, nil)
	binding, err := service.BindRTC(context.Background(), devices.RTCBindingRequest{InputPresent: true, BypassSelfHearing: true})
	if err != nil {
		t.Fatalf("BindRTC() error = %v", err)
	}
	if binding == nil || binding.Inferencer() == nil {
		t.Fatal("BindRTC() returned no public binding inferencer")
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
