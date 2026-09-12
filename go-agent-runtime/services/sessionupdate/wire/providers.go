//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the private sessionupdate implementation while
// exposing only the host-neutral public service contract.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate/internal/service"
)

// NewService assembles the session-update service.
func NewService() sessionupdate.Service {
	wire.Build(service.New, wire.Bind(new(sessionupdate.Service), new(*service.Service)))
	return nil
}
