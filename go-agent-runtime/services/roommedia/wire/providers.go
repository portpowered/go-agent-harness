//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room-media service from public dependencies.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia/internal/service"
)

// Dependencies contains only the host's explicit timing source.
type Dependencies struct {
	Clock roommedia.Clock
}

func NewService(dependencies Dependencies) roommedia.Service {
	wire.Build(newService, wire.Bind(new(roommedia.Service), new(*service.Service)))
	return nil
}

func newService(dependencies Dependencies) *service.Service {
	return service.New(service.Dependencies{Clock: dependencies.Clock})
}
