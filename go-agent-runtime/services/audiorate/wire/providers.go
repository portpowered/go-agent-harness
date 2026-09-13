//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the audio-rate service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate/internal/service"
)

// NewService assembles the private audio-rate implementation and returns only
// the public service contract.
func NewService() audiorate.Service {
	wire.Build(service.New, wire.Bind(new(audiorate.Service), new(*service.Service)))
	return nil
}
