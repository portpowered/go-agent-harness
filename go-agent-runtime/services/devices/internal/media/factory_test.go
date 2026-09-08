package media

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestFactoryClosesCaptureWhenPlaybackAdmissionFails(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{inner: inner, failID: "virtual:output"}
	factory := NewFactory(registry, mixer.DefaultFormat())
	_, err = factory.Open(context.Background(), devices.Request{
		InputDevice:     "virtual:input",
		OutputDevice:    "virtual:output",
		CaptureEnabled:  true,
		PlaybackEnabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "open output device") {
		t.Fatalf("OpenMedia error = %v, want output admission error", err)
	}
	if got := inner.Observations().ReleaseCount; got != 1 {
		t.Fatalf("released handles = %d, want capture cleanup after output failure", got)
	}
}

func TestFactoryRejectsUnsupportedShapeBeforeOpen(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{inner: inner}
	factory := NewFactory(registry, mixer.Format{SampleRate: 24000, Channels: 2})
	_, err = factory.Open(context.Background(), devices.Request{CaptureEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "requires mono format") {
		t.Fatalf("OpenMedia error = %v, want mono format validation", err)
	}
	if got := len(registry.opened); got != 0 {
		t.Fatalf("opened devices = %d, want no admission for invalid format", got)
	}
}

func TestFactoryRejectsCancelledAdmission(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{inner: inner}
	factory := NewFactory(registry, mixer.DefaultFormat())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = factory.Open(ctx, devices.Request{CaptureEnabled: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenMedia error = %v, want context.Canceled", err)
	}
	if got := len(registry.opened); got != 0 {
		t.Fatalf("opened devices = %d, want no admission after cancellation", got)
	}
}

func TestFactoryHandleOwnsBothWorkersAndClosesOnce(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(inner, mixer.DefaultFormat())
	handle, err := factory.Open(context.Background(), devices.Request{
		InputDevice:     "virtual:input",
		OutputDevice:    "virtual:output",
		CaptureEnabled:  true,
		PlaybackEnabled: true,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if handle == nil || handle.Media().Capture == nil || handle.Media().Playback == nil {
		t.Fatalf("Open() returned incomplete media handle: %#v", handle)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := inner.Observations().ReleaseCount; got != 2 {
		t.Fatalf("released handles = %d, want one release per worker", got)
	}
}

func TestFactoryAdmitsEachDirectionIndependently(t *testing.T) {
	tests := []struct {
		name       string
		enable     devices.Request
		wantInput  bool
		wantOutput bool
		wantOpen   int
	}{
		{name: "capture only", enable: devices.Request{CaptureEnabled: true}, wantInput: true, wantOpen: 1},
		{name: "playback only", enable: devices.Request{PlaybackEnabled: true}, wantOutput: true, wantOpen: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
			if err != nil {
				t.Fatal(err)
			}
			test.enable.InputDevice = "virtual:input"
			test.enable.OutputDevice = "virtual:output"
			handle, err := NewFactory(inner, mixer.DefaultFormat()).Open(context.Background(), test.enable)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			ports := handle.Media()
			if (ports.Capture != nil) != test.wantInput || (ports.Playback != nil) != test.wantOutput {
				t.Fatalf("ports = capture %v playback %v", ports.Capture != nil, ports.Playback != nil)
			}
			if err := handle.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if got := inner.Observations().OpenCount; got != test.wantOpen {
				t.Fatalf("opened handles = %d, want %d", got, test.wantOpen)
			}
		})
	}
}

func TestFactoryRejectsDirectionlessAndNegativeRequests(t *testing.T) {
	inner, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatal(err)
	}
	factory := NewFactory(inner, mixer.DefaultFormat())
	for _, request := range []devices.Request{
		{},
		{CaptureEnabled: true, SampleRate: -1},
		{PlaybackEnabled: true, Channels: -1},
	} {
		if _, err := factory.Open(context.Background(), request); !errors.Is(err, devices.ErrInvalidRequest) {
			t.Fatalf("Open(%+v) error = %v, want devices.ErrInvalidRequest", request, err)
		}
	}
	if got := inner.Observations().OpenCount; got != 0 {
		t.Fatalf("opened handles = %d, want no admission for invalid requests", got)
	}
}

func TestFactoryRejectsInvalidRemoteEndpoint(t *testing.T) {
	factory := NewFactory(devicegw.NewPlatformDeviceRegistry(), mixer.DefaultFormat())
	_, err := factory.Open(context.Background(), devices.Request{PlaybackEnabled: true, RemoteEndpoint: "192.0.2.10:19090"})
	if !errors.Is(err, devicegw.ErrRemoteDeviceServerEndpoint) {
		t.Fatalf("Open remote error = %v, want ErrRemoteDeviceServerEndpoint", err)
	}
}

type registryStub struct {
	inner  devicegw.DeviceRegistry
	failID devicegw.DeviceID
	opened []devicegw.DeviceID
}

func (r *registryStub) List() ([]devicegw.Device, error) { return r.inner.List() }

func (r *registryStub) Default(direction devicegw.Direction) (devicegw.Device, error) {
	return r.inner.Default(direction)
}

func (r *registryStub) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	if id == r.failID {
		return nil, errors.New("injected output admission failure")
	}
	handle, err := r.inner.Open(id)
	if err != nil {
		return nil, err
	}
	r.opened = append(r.opened, id)
	return handle, nil
}

var _ devicegw.DeviceRegistry = (*registryStub)(nil)
