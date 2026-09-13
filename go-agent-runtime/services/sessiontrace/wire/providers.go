//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the session trace service as a whole.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/internal/service"
)

func NewService() sessiontrace.Service {
	wire.Build(service.New, wire.Bind(new(sessiontrace.Service), new(*service.Service)))
	return nil
}
