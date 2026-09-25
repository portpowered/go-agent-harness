package roomhost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type stubRegistry struct {
	devices []devicegw.Device
	listErr error
}

func (r stubRegistry) List() ([]devicegw.Device, error) { return r.devices, r.listErr }

func (r stubRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	for _, device := range r.devices {
		if device.Direction == direction {
			return device, nil
		}
	}
	return devicegw.Device{}, devicegw.NewNoDefaultDeviceError(direction)
}

func (stubRegistry) Open(id devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	return nil, devicegw.NewDeviceNotFoundError(id)
}

func TestRunPlanOptionsProjectsHostDevicesAndPaths(t *testing.T) {
	input, err := devicegw.NewDevice("fake", "mic", "Mic", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	registry := stubRegistry{devices: []devicegw.Device{input, {ID: "invalid"}}}
	options := RunPlanOptions(Paths{Config: "room.json", Replay: "bundle", ConfigDir: t.TempDir()}, registry)
	if options.ReplayPath != "bundle" || options.Launch.ConfigPath != "room.json" || options.Launch.Devices == nil {
		t.Fatalf("run plan options = %+v, want projected paths and host devices", options)
	}
	listed, err := options.Launch.Devices.List()
	if err != nil || len(listed) != 1 || listed[0].ID != input.ID || listed[0].Direction != rooms.LaunchDeviceInput {
		t.Fatalf("listed devices = %+v / %v, want only the valid host device", listed, err)
	}
	if device, err := options.Launch.Devices.Default(rooms.LaunchDeviceInput); err != nil || device.ID != input.ID {
		t.Fatalf("default input = %+v / %v, want the host default", device, err)
	}
	if _, err := options.Launch.Devices.Default(rooms.LaunchDeviceOutput); !errors.Is(err, devicegw.ErrNoDefaultDevice) {
		t.Fatalf("missing default output = %v, want the host cause", err)
	}
	if without := RunPlanOptions(Paths{}, nil); without.Launch.Devices != nil {
		t.Fatal("nil registry produced a device port")
	}
	failing := RunPlanOptions(Paths{}, stubRegistry{listErr: errors.New("offline")})
	if _, err := failing.Launch.Devices.List(); err == nil {
		t.Fatal("registry listing failure was hidden")
	}
}

func TestConfigCredentialReadsOnlyTheBareRoomKey(t *testing.T) {
	lookup := ConfigCredential(filepath.Join(t.TempDir(), "config"))
	for _, name := range []string{rooms.DefaultOpenAIAPIKeyEnv, "OTHER_KEY"} {
		if value, err := lookup(name); err != nil || value != "" {
			t.Fatalf("empty config credential %q = %q / %v, want absent", name, value, err)
		}
	}
}

func TestBrowserWatchProjectsSemanticThenLegacyEvents(t *testing.T) {
	if BrowserWatch(nil, nil) != nil {
		t.Fatal("absent browser streams produced a watch")
	}
	semantic := BrowserWatch(func(context.Context) <-chan webmcp.BrowserEvent {
		events := make(chan webmcp.BrowserEvent, 1)
		events <- webmcp.BrowserEvent{Type: "tools_added", Sequence: 7, Status: "ready", ToolCount: 2, ToolCountKnown: true}
		close(events)
		return events
	}, nil)
	got := drainBrowserWatch(t, semantic)
	if got.Type != "tools_added" || got.Sequence != 7 || got.State != "ready" || got.Status != "ready" || got.ToolCount != 2 {
		t.Fatalf("semantic event = %+v, want projected fields", got)
	}
	legacy := BrowserWatch(nil, func(context.Context) <-chan webmcp.BrokerEvent {
		events := make(chan webmcp.BrokerEvent, 1)
		events <- webmcp.BrokerEvent{Type: "invocation", Sequence: 3, ToolName: "click", State: "running"}
		close(events)
		return events
	})
	got = drainBrowserWatch(t, legacy)
	if got.Type != "invocation" || got.Sequence != 3 || got.ToolName != "click" || got.State != "running" {
		t.Fatalf("legacy event = %+v, want projected fields", got)
	}
}

func drainBrowserWatch(t *testing.T, watch func(context.Context) <-chan rooms.BrowserEvent) rooms.BrowserEvent {
	t.Helper()
	select {
	case event := <-watch(context.Background()):
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("browser watch delivered nothing")
		return rooms.BrowserEvent{}
	}
}
