//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the tools service. The
// generated graph returns the public service contract while keeping the
// registry implementation private to this service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/service"
)

// NewService creates the inert tools service. Tool resources are resolved per
// request by the returned service rather than during graph construction.
func NewService() tools.Service {
	wire.Build(service.New, wire.Bind(new(tools.Service), new(*service.Service)))
	return nil
}
