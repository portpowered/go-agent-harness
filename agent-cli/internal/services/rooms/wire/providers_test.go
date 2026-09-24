package wire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/wire"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type deviceServiceStub struct{}

func (deviceServiceStub) Open(context.Context, runtimeDevices.Request) (runtimeDevices.Handle, error) {
	return nil, runtimeDevices.ErrUnavailable
}

func (deviceServiceStub) BindRTC(context.Context, runtimeDevices.RTCBindingRequest) (runtimeDevices.RTCBinding, error) {
	return nil, runtimeDevices.ErrUnavailable
}

func TestNewServiceDelegatesPublicRoomContract(t *testing.T) {
	replay := runtimeReplayWire.NewService(replaywire.NewService())
	service := NewService(Dependencies{Replay: replay, Devices: deviceServiceStub{}})
	if service == nil {
		t.Fatal("NewService() returned nil")
	}
	replayOwner, ok := service.(interface{ ReplayService() roomreplay.Service })
	if !ok || replayOwner.ReplayService() != replay {
		t.Fatalf("ReplayService() = %v, want injected replay owner", replayOwner)
	}

	if _, err := service.LoadReplayPlan(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("LoadReplayPlan() succeeded for missing bundle")
	}
	plan := runtimeRooms.RoomReplayPlan{BundlePath: filepath.Join(t.TempDir(), "bundle")}
	if err := service.ValidateReplayOutput(plan, filepath.Join(t.TempDir(), "output")); err != nil {
		t.Fatalf("ValidateReplayOutput() error = %v", err)
	}
	if manifest := service.ReplayManifest(plan); manifest.SchemaVersion != runtimeRooms.SchemaVersion {
		t.Fatalf("ReplayManifest() schema = %d, want %d", manifest.SchemaVersion, runtimeRooms.SchemaVersion)
	}
	if err := service.ValidateEvidenceOutput(""); err == nil {
		t.Fatal("ValidateEvidenceOutput() accepted an empty destination")
	}
	runDir, err := service.CreateFreshRunDirectory(t.TempDir())
	if err != nil {
		t.Fatalf("CreateFreshRunDirectory() error = %v", err)
	}
	if info, statErr := os.Stat(runDir); statErr != nil || !info.IsDir() {
		t.Fatalf("run directory = %q, stat error = %v", runDir, statErr)
	}
	if _, err := service.ResolveLaunchPlan(runtimeRooms.RoomLaunchOptions{}); err == nil {
		t.Fatal("ResolveLaunchPlan() succeeded without a configured path or device registry")
	}
	if _, err := service.Run(context.Background(), nil, runtimeRooms.RoomRunOptions{}); err == nil {
		t.Fatal("Run() succeeded without a valid room manifest")
	}
}

func TestServiceReturnsUnavailableAfterHostGraphIsAbsent(t *testing.T) {
	var service *service
	if service.ReplayService() != nil {
		t.Fatal("nil ReplayService() returned an owner")
	}
	if _, err := service.Run(context.Background(), nil, runtimeRooms.RoomRunOptions{}); !errors.Is(err, runtimeRooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil Run() error = %v, want unavailable", err)
	}
	if _, err := service.ResolveLaunchPlan(runtimeRooms.RoomLaunchOptions{}); !errors.Is(err, runtimeRooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil ResolveLaunchPlan() error = %v, want unavailable", err)
	}
	if _, err := service.LoadReplayPlan("bundle"); !errors.Is(err, runtimeRooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil LoadReplayPlan() error = %v, want unavailable", err)
	}
	if !errors.Is(service.ValidateReplayOutput(runtimeRooms.RoomReplayPlan{}, "output"), runtimeRooms.ErrRoomServiceUnavailable) {
		t.Fatal("nil ValidateReplayOutput() did not report unavailable")
	}
	if service.ReplayManifest(runtimeRooms.RoomReplayPlan{}).SchemaVersion != 0 {
		t.Fatal("nil ReplayManifest() returned a populated manifest")
	}
	if !errors.Is(service.ValidateEvidenceOutput("output"), runtimeRooms.ErrRoomServiceUnavailable) {
		t.Fatal("nil ValidateEvidenceOutput() did not report unavailable")
	}
	if _, err := service.CreateFreshRunDirectory("config"); !errors.Is(err, runtimeRooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil CreateFreshRunDirectory() error = %v, want unavailable", err)
	}
}
