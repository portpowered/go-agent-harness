// Package service contains the room service coordinator. Admission helpers
// are concrete and usable now; live participant execution is supplied through
// a narrow function port until the session audio/control contract is ready.
package service

import (
	"context"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
)

type Dependencies struct {
	Planner  planning.Planner
	Evidence evidence.Loader
	Runner   lifecycle.Runner
}

type Service struct {
	planner  planning.Planner
	evidence evidence.Loader
	runner   lifecycle.Runner
}

func New(dependencies Dependencies) rooms.Service {
	return &Service{planner: dependencies.Planner, evidence: dependencies.Evidence, runner: dependencies.Runner}
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
	}
	if strings.TrimSpace(request.OutputDir) != "" {
		var err error
		if request.ReplayPlan != nil {
			err = evidence.ValidateOutput(*request.ReplayPlan, request.OutputDir)
		} else {
			err = evidence.ValidateEvidenceOutput(request.OutputDir)
		}
		if err != nil {
			return rooms.RoomResult{}, err
		}
	}
	return s.runner.Run(ctx, out, request)
}

func (s *Service) ResolveLaunchPlan(options rooms.RoomLaunchOptions) (rooms.RoomLaunchPlan, error) {
	return s.planner.Resolve(options)
}

func (s *Service) LoadReplayPlan(bundle string) (rooms.RoomReplayPlan, error) {
	return s.evidence.Load(bundle)
}

func (s *Service) ValidateReplayOutput(plan rooms.RoomReplayPlan, destination string) error {
	return evidence.ValidateOutput(plan, destination)
}

func (s *Service) ValidateEvidenceOutput(destination string) error {
	return evidence.ValidateEvidenceOutput(destination)
}

func (s *Service) CreateFreshRunDirectory(configDir string) (string, error) {
	return evidence.CreateFreshRunDirectory(configDir)
}

var _ rooms.Service = (*Service)(nil)
