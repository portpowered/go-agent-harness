//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room-replay-bundle admission service without
// exposing its parser or filesystem implementation to hosts.
package wire

import (
	"github.com/google/wire"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/internal/service"
)

func NewService() roomreplay.Service {
	wire.Build(service.New, replaywire.NewService, wire.Bind(new(roomreplay.Service), new(*service.Service)))
	return nil
}
