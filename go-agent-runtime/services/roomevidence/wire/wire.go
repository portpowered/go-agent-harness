//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the room evidence service. The generated graph
// returns only the public contract; implementation types remain private.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/service"
)

// NewServiceWithOptions composes a service with caller-selected options.
// Zero-valued fields keep the production defaults.
func NewServiceWithOptions(options roomevidence.ServiceOptions) roomevidence.Service {
	wire.Build(service.New, wire.Bind(new(roomevidence.Service), new(*service.Service)))
	return nil
}
