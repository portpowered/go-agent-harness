//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room admission graph. Host-specific device and
// session composition binds only the public room dependencies here; planning,
// evidence, and lifecycle implementations stay private to this service.
package wire

import (
	"github.com/google/wire"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/service"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type Dependencies struct {
	Live   session.LiveService
	Media  rooms.MediaFactory
	Replay roomreplay.Service
	Clock  platformclock.Scheduler
}

func NewService(dependencies Dependencies) rooms.Service {
	wire.Build(newPlanner, newEvidenceService, newLatencyService, newRunner, newServiceDependencies, service.New)
	return nil
}

func newPlanner() planning.Planner { return planning.New() }

func newEvidenceService() rooms.EvidenceService { return roomevidencewire.NewService() }

func newLatencyService() rooms.LatencyService { return roomevidencewire.NewLatencyService() }

func newRunner(dependencies Dependencies, evidenceService rooms.EvidenceService, latency rooms.LatencyService) lifecycle.Runner {
	return lifecycle.New(lifecycle.Dependencies{
		Live: dependencies.Live, Media: dependencies.Media, Clock: dependencies.Clock,
		Evidence: evidenceService, Latency: latency,
	})
}

func newServiceDependencies(planner planning.Planner, runner lifecycle.Runner, dependencies Dependencies, evidenceService rooms.EvidenceService) service.Dependencies {
	return service.Dependencies{Planner: planner, Replay: dependencies.Replay, Runner: runner, Evidence: evidenceService}
}
