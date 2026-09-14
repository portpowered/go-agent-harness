//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire constructs the room-error service behind its public contract.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionroomerrors/internal/service"
)

func NewService() sessionroomerrors.Service {
	wire.Build(service.New, wire.Bind(new(sessionroomerrors.Service), new(*service.Service)))
	return nil
}
