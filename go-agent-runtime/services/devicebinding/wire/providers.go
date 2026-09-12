//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the public devicebinding service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devicebinding/internal/service"
)

// NewService returns a stateless service whose Open method performs admission.
func NewService() devicebinding.Service {
	wire.Build(service.New, wire.Bind(new(devicebinding.Service), new(*service.Service)))
	return nil
}
