//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room capability service from public dependencies.
// Host configuration and browser protocol adapters are supplied as values to
// the returned service; this graph never reads process state.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities"
	roomservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomcapabilities/internal/service"
)

// NewService constructs an inert participant capability service. Every call to
// Compose receives explicit participant-local surfaces and returns independent
// snapshots and lifecycle callbacks.
func NewService() roomcapabilities.Service {
	wire.Build(roomservice.New, wire.Bind(new(roomcapabilities.Service), new(*roomservice.Service)))
	return nil
}
