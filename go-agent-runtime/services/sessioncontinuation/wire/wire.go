//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for sessioncontinuation.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation/internal/service"
)

// NewService constructs the inert continuation policy service.
func NewService() sessioncontinuation.Service {
	wire.Build(service.New, wire.Bind(new(sessioncontinuation.Service), new(*service.Service)))
	return nil
}
