//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the public toolpublication service from explicit
// host dependencies. The publisher implementation remains private.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/toolpublication"
)

// NewService constructs the reusable publication service.
func NewService(dependencies Dependencies) toolpublication.Service {
	wire.Build(newService)
	return nil
}
