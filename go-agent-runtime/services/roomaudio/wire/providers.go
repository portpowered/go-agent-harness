//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the host-neutral room audio projection service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio/internal/service"
)

func NewService() roomaudio.Service {
	wire.Build(service.New, wire.Bind(new(roomaudio.Service), new(*service.Service)))
	return nil
}
