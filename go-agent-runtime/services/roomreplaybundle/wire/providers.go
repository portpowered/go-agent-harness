//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the room-replay-bundle admission service without
// exposing its parser or filesystem implementation to hosts.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle/internal/service"
)

func NewService() roomreplaybundle.Service {
	wire.Build(service.New, wire.Bind(new(roomreplaybundle.Service), new(*service.Service)))
	return nil
}
