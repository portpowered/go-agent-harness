//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the terminal-outcome service without exposing its
// mutable implementation package to hosts.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome/internal/service"
)

// NewService assembles one host-neutral terminal-outcome service.
func NewService() terminaloutcome.Service {
	wire.Build(service.New, wire.Bind(new(terminaloutcome.Service), new(*service.Service)))
	return nil
}
