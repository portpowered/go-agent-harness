//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the session diagnostics reducer behind its public
// contract. Construction is inert and each call creates independent state.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics/internal/service"
)

// NewService constructs one response lifecycle reducer.
func NewService(options sessiondiagnostics.Options) sessiondiagnostics.Service {
	wire.Build(service.New)
	return nil
}
