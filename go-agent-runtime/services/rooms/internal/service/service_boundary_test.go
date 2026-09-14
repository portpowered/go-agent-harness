package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
)

func TestServicePublicContractHandlesUnavailableDependenciesAndBounds(t *testing.T) {
	var nilService *Service
	var public rooms.Service = nilService
	if _, err := public.Run(context.Background(), io.Discard, rooms.RoomRunOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service Run error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := public.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service LoadReplayPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := public.ValidateReplayOutput(rooms.RoomReplayPlan{}, "destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("nil service ValidateReplayOutput error = %v, want ErrRoomServiceUnavailable", err)
	}
	if got := public.ReplayManifest(rooms.RoomReplayPlan{}); got.SchemaVersion != rooms.SchemaVersion {
		t.Fatalf("nil service ReplayManifest schema = %d, want %d", got.SchemaVersion, rooms.SchemaVersion)
	}

	runtimeService := New(Dependencies{
		Planner:  planning.New(),
		Evidence: &evidenceStub{},
		Runner:   lifecycle.New(lifecycle.Dependencies{}),
	})
	manifest := rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 1},
		Participants: []rooms.Participant{
			{Kind: rooms.ParticipantKindAgent, ID: "opener", SystemPrompt: "start", OpeningPrompt: "hello", Provider: "openai", Model: "realtime", APIKeyEnv: "OPENAI_KEY", Tools: []string{}},
			{Kind: rooms.ParticipantKindAgent, ID: "responder", SystemPrompt: "answer", Provider: "openai", Model: "realtime", APIKeyEnv: "OPENAI_KEY", Tools: []string{}},
		},
	}
	if _, err := runtimeService.Run(context.Background(), io.Discard, rooms.RoomRunOptions{Manifest: manifest}); !errors.Is(err, rooms.ErrRoomClockUnavailable) {
		t.Fatalf("missing clock Run error = %v, want ErrRoomClockUnavailable", err)
	}
	if _, err := runtimeService.Run(context.Background(), io.Discard, rooms.RoomRunOptions{Manifest: manifest, ReplayPath: "bundle"}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay Run error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := runtimeService.ResolveLaunchPlan(rooms.RoomLaunchOptions{}); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("bare ResolveLaunchPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if _, err := runtimeService.LoadReplayPlan("bundle"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay LoadReplayPlan error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := runtimeService.ValidateReplayOutput(rooms.RoomReplayPlan{}, "destination"); !errors.Is(err, rooms.ErrRoomServiceUnavailable) {
		t.Fatalf("missing replay ValidateReplayOutput error = %v, want ErrRoomServiceUnavailable", err)
	}
	if err := runtimeService.ValidateEvidenceOutput("destination"); err != nil {
		t.Fatalf("ValidateEvidenceOutput: %v", err)
	}
	runDir, err := runtimeService.CreateFreshRunDirectory("config")
	if err != nil {
		t.Fatalf("CreateFreshRunDirectory: %v", err)
	}
	if runDir != "fresh-run" {
		t.Fatalf("fresh run directory = %q, want fresh-run", runDir)
	}
	if got := runtimeService.ReplayManifest(rooms.RoomReplayPlan{}); got.SchemaVersion != rooms.SchemaVersion {
		t.Fatalf("ReplayManifest schema = %d, want %d", got.SchemaVersion, rooms.SchemaVersion)
	}
}

type evidenceStub struct {
	roomevidence.Service
}

func (*evidenceStub) ValidateEvidenceOutput(string) error { return nil }

func (*evidenceStub) CreateFreshRunDirectory(string) (string, error) { return "fresh-run", nil }
