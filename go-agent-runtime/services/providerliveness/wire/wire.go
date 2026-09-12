//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for provider liveness.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness/internal/service"
)

// NewService assembles one isolated provider-progress service.
func NewService(deps providerliveness.Dependencies) providerliveness.Service {
	wire.Build(service.New, wire.Bind(new(providerliveness.Service), new(*service.Service)))
	return nil
}
