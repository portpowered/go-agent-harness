//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room admission graph. Host-specific device and
// session composition binds only the public room dependencies here; planning,
// evidence, and lifecycle implementations stay private to this service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	roomreplaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/errorpolicy"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/service"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type Dependencies struct {
	Live     session.LiveService
	Media    rooms.MediaFactory
	Replay   roomreplay.Service
	Clock    platformclock.Scheduler
	Evidence roomevidence.Service
	Tools    runtimeTools.Service
}

func NewService(dependencies Dependencies) rooms.Service {
	wire.Build(newPlanner, newReplay, newEvidence, newFailureService, newRunner, newServiceDependencies, service.New)
	return nil
}

func newPlanner() planning.Planner { return planning.New() }

func newReplay(dependencies Dependencies) roomreplay.Service {
	if dependencies.Replay != nil {
		return dependencies.Replay
	}
	return roomreplaywire.NewService()
}

func newEvidence(dependencies Dependencies) roomevidence.Service { return dependencies.Evidence }

func newFailureService() rooms.FailureService { return errorpolicy.New() }

func newRunner(dependencies Dependencies, failure rooms.FailureService) lifecycle.Runner {
	return lifecycle.New(lifecycle.Dependencies{
		Live: dependencies.Live, Media: dependencies.Media, Clock: dependencies.Clock,
		Failure: failure, Evidence: dependencies.Evidence, Tools: dependencies.Tools,
	})
}

func newServiceDependencies(planner planning.Planner, replayService roomreplay.Service, evidenceService roomevidence.Service, runner lifecycle.Runner) service.Dependencies {
	return service.Dependencies{Planner: planner, Replay: replayService, Evidence: evidenceService, Runner: runner}
}
