package wire

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func TestServiceBoundaryPreservesUnavailableBehavior(t *testing.T) {
	var empty service
	if empty.ReplayService() != nil {
		t.Fatal("empty service exposed a replay owner")
	}
	if _, err := empty.Run(context.Background(), io.Discard, rooms.RoomRunOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("empty service Run error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := empty.ResolveLaunchPlan(rooms.RoomLaunchOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("empty service ResolveLaunchPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := empty.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("empty service LoadReplayPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if got := empty.ReplayManifest(rooms.RoomReplayPlan{}); got.SchemaVersion != 0 || len(got.Participants) != 0 {
		t.Fatalf("empty service ReplayManifest = %#v, want zero manifest", got)
	}
	if err := empty.ValidateReplayOutput(rooms.RoomReplayPlan{}, "destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("empty service ValidateReplayOutput error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := empty.ValidateEvidenceOutput("destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("empty service ValidateEvidenceOutput error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := empty.CreateFreshRunDirectory("config"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("empty service CreateFreshRunDirectory error = %v, want ErrRoomServiceUnavailable", err)
	}

	var nilService *service
	if _, err := nilService.Run(context.Background(), io.Discard, rooms.RoomRunOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service Run error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := nilService.ResolveLaunchPlan(rooms.RoomLaunchOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service ResolveLaunchPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := nilService.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service LoadReplayPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if nilService.ReplayManifest(rooms.RoomReplayPlan{}).SchemaVersion != 0 {
		t.Fatal("nil service ReplayManifest returned a non-zero manifest")
	}
	if err := nilService.ValidateReplayOutput(rooms.RoomReplayPlan{}, "destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service ValidateReplayOutput error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := nilService.ValidateEvidenceOutput("destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service ValidateEvidenceOutput error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := nilService.CreateFreshRunDirectory("config"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service CreateFreshRunDirectory error = %v, want ErrRoomServiceUnavailable", err)
	}
}

func TestNewServiceDelegatesToRuntimeContract(t *testing.T) {
	public := NewService(Dependencies{})
	if public == nil {
		t.Fatal("NewService returned nil")
	}
	if _, err := public.Run(context.Background(), io.Discard, rooms.RoomRunOptions{}); !errors.Is(err, rooms.ErrUnsupportedSchema) {
		t.Fatalf("zero manifest Run error = %v, want ErrUnsupportedSchema", err)
	}
	if _, err := public.ResolveLaunchPlan(rooms.RoomLaunchOptions{}); err == nil {
		t.Fatal("bare launch unexpectedly succeeded without a device registry")
	}
	if _, err := public.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay service error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := public.ValidateReplayOutput(rooms.RoomReplayPlan{}, "destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay service validation error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := public.ValidateEvidenceOutput(""); err == nil {
		t.Fatal("empty evidence destination unexpectedly succeeded")
	}
	if _, err := public.CreateFreshRunDirectory(t.TempDir()); err != nil {
		t.Fatalf("CreateFreshRunDirectory: %v", err)
	}
}
