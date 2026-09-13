//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the audio-output service behind its public contract.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput/internal/service"
)

func NewService() audiooutput.Service {
	wire.Build(service.New, wire.Bind(new(audiooutput.Service), new(*service.Service)))
	return nil
}
