//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the sessionlive service without exposing its private
// implementation package to hosts.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive/internal/service"
)

// NewService returns a fresh stateless service graph. Invocation state is
// supplied to Run, so separate calls cannot share timers, loops, or buffers.
func NewService() sessionlive.Service {
	wire.Build(service.New, wire.Bind(new(sessionlive.Service), new(*service.Service)))
	return nil
}
