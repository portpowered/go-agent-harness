//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session finalization service without exposing
// its private implementation package to hosts.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfinalization/internal/service"
)

// NewService returns the reusable finalization contract.
func NewService() sessionfinalization.Service {
	wire.Build(service.New, wire.Bind(new(sessionfinalization.Service), new(*service.Service)))
	return nil
}
