//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the metrics replay service while keeping its
// implementation private to this service boundary.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay/internal/service"
)

// NewService assembles one invocation-neutral metrics replay service from
// explicit host dependencies.
func NewService(deps metricsreplay.Dependencies) metricsreplay.Service {
	wire.Build(newService, wire.Bind(new(metricsreplay.Service), new(*service.Service)))
	return nil
}

func newService(deps metricsreplay.Dependencies) *service.Service {
	return service.New(service.Dependencies{
		Clock:   deps.Clock,
		Runner:  deps.Runner,
		Loader:  deps.Loader,
		NewSink: deps.NewSink,
	})
}
