package wire

import (
	"bytes"
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

func TestFileMediaServiceOpensStdoutSinkAndSkipsEmptyRequests(t *testing.T) {
	service := NewFileMediaService()
	if handle, err := service.OpenFileMedia(devices.FileMediaRequest{}); handle != nil || err != nil {
		t.Fatalf("empty request = (%v, %v), want no media", handle, err)
	}
	var stdout bytes.Buffer
	handle, err := service.OpenFileMedia(devices.FileMediaRequest{OutputPath: "-", Stdout: &stdout, OutputSampleRate: 24000})
	if err != nil {
		t.Fatalf("OpenFileMedia: %v", err)
	}
	if output := handle.Media().Output; output == nil || !output.Continuous || output.SampleRate != 24000 {
		t.Fatalf("stdout output = %+v, want continuous 24 kHz sink", output)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
