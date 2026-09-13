//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for room-audio diagnostics.
// The generated graph returns the public contract while the ledger remains
// private to the service package.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics/internal/service"
)

// NewService constructs one isolated participant ledger.
func NewService(options roomaudiodiagnostics.Options) roomaudiodiagnostics.Service {
	wire.Build(service.New, wire.Bind(new(roomaudiodiagnostics.Service), new(*service.Service)))
	return nil
}
