//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room admission graph. Host-specific device and
// session composition binds only the public room dependencies here; planning,
// evidence, and lifecycle implementations stay private to this service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/evidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/planning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/service"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type Dependencies struct {
	Live  session.LiveService
	Media rooms.MediaFactory
	Clock platformclock.Scheduler
}

func NewService(dependencies Dependencies) rooms.Service {
	wire.Build(newPlanner, newEvidence, newRunner, newServiceDependencies, service.New)
	return nil
}

func newPlanner() planning.Planner { return planning.New() }

func newEvidence() evidence.Loader { return evidence.New() }

func newRunner(dependencies Dependencies) lifecycle.Runner {
	return lifecycle.New(lifecycle.Dependencies{
		Live: dependencies.Live, Media: dependencies.Media, Clock: dependencies.Clock,
	})
}

func newServiceDependencies(planner planning.Planner, evidenceLoader evidence.Loader, runner lifecycle.Runner) service.Dependencies {
	return service.Dependencies{Planner: planner, Evidence: evidenceLoader, Runner: runner}
}
