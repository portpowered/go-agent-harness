//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the provider-session service while keeping its
// replay and provider implementation private.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession/internal/service"
)

type Dependencies = providersession.Dependencies

func NewService(deps Dependencies) providersession.Service {
	wire.Build(service.New, wire.Bind(new(providersession.Service), new(*service.Service)))
	return nil
}
