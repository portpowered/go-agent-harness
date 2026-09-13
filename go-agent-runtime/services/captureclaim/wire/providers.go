//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the capture-claim
// service. Generated construction returns the public contract and keeps the
// filesystem implementation private.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim/internal/service"
)

// NewService constructs an inert capture-claim service from explicit seams.
func NewService(deps Dependencies) captureclaim.Service {
	wire.Build(service.New, wire.Bind(new(captureclaim.Service), new(*service.Service)))
	return nil
}
