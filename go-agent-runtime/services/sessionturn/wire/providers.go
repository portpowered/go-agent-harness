//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session-turn implementation behind its public
// contract. Hosts may inject only the service's neutral allocation port.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/service"
)

type Dependencies struct {
	Allocator sessionturn.Allocator
}

func NewService(deps Dependencies) sessionturn.Service {
	wire.Build(newServiceDependencies, service.New, wire.Bind(new(sessionturn.Service), new(*service.Service)))
	return nil
}

func newServiceDependencies(deps Dependencies) service.Dependencies {
	return service.Dependencies{Allocator: deps.Allocator}
}
