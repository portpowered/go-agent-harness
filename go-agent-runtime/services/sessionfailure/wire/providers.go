//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session-failure service without exposing its
// implementation package to hosts and embedders.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure/internal/service"
)

// NewService assembles one invocation-local failure service.
func NewService(dependencies sessionfailure.Dependencies) sessionfailure.Service {
	wire.Build(service.New, wire.Bind(new(sessionfailure.Service), new(*service.Service)))
	return nil
}
