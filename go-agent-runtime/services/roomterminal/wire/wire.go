//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the room terminal service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal/internal/service"
)

// NewService creates a stateless room terminal policy service.
func NewService() roomterminal.Service {
	wire.Build(service.New, wire.Bind(new(roomterminal.Service), new(*service.Service)))
	return nil
}
