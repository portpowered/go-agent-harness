// Package service contains the room service coordinator. Admission helpers
// are concrete and usable now; live participant execution is supplied through
// a narrow function port until the session audio/control contract is ready.
package service

import (
	"context"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	roommanifest "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/manifest"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
)

type Dependencies struct {
	Planner planning.Planner
	Replay  roomreplay.Service
	Runner  lifecycle.Runner
}

type Service struct {
	planner planning.Planner
	replay  roomreplay.Service
	runner  lifecycle.Runner
}

func New(dependencies Dependencies) rooms.Service {
	return &Service{planner: dependencies.Planner, replay: dependencies.Replay, runner: dependencies.Runner}
}

func (s *Service) Run(ctx context.Context, out io.Writer, request rooms.RoomRunOptions) (rooms.RoomResult, error) {
	if s == nil {
		return rooms.RoomResult{}, rooms.ErrRoomServiceUnavailable
	}
	if request.ReplayPlan == nil && strings.TrimSpace(request.ReplayPath) != "" {
		plan, err := s.LoadReplayPlan(request.ReplayPath)
		if err != nil {
			return rooms.RoomResult{}, err
		}
		request.ReplayPlan = &plan
		if request.Manifest.SchemaVersion == 0 && len(request.Manifest.Participants) == 0 {
			request.Manifest = s.ReplayManifest(plan)
		}
	}
	if strings.TrimSpace(request.OutputDir) != "" {
		if request.ReplayPlan != nil {
			if err := s.ValidateReplayOutput(*request.ReplayPlan, request.OutputDir); err != nil {
				return rooms.RoomResult{}, err
			}
		}
		if err := evidence.ValidateEvidenceOutput(request.OutputDir); err != nil {
			return rooms.RoomResult{}, err
		}
	}
	return s.runner.Run(ctx, out, request)
}

func (s *Service) ResolveLaunchPlan(options rooms.RoomLaunchOptions) (rooms.RoomLaunchPlan, error) {
	return s.planner.Resolve(options)
}

func (s *Service) LoadReplayPlan(bundle string) (rooms.RoomReplayPlan, error) {
	if s == nil || s.replay == nil {
		return rooms.RoomReplayPlan{}, rooms.ErrRoomServiceUnavailable
	}
	return s.replay.Load(bundle)
}

func (s *Service) ReplayManifest(plan rooms.RoomReplayPlan) rooms.Manifest {
	return roommanifest.FromReplay(plan)
}

func (s *Service) ValidateReplayOutput(plan rooms.RoomReplayPlan, destination string) error {
	if s == nil || s.replay == nil {
		return rooms.ErrRoomServiceUnavailable
	}
	return s.replay.ValidateOutput(plan, destination)
}

func (s *Service) ValidateEvidenceOutput(destination string) error {
	return evidence.ValidateEvidenceOutput(destination)
}

func (s *Service) CreateFreshRunDirectory(configDir string) (string, error) {
	return evidence.CreateFreshRunDirectory(configDir)
}

var _ rooms.Service = (*Service)(nil)
