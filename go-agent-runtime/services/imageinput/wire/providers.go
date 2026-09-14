//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only construction surface for the image-input service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput/internal/service"
)

// NewService assembles image preparation and publication from the host-owned
// content loader. No filesystem or provider configuration is discovered here.
func NewService(loader imageinput.ContentLoader) imageinput.Service {
	wire.Build(service.New, wire.Bind(new(imageinput.Service), new(*service.Service)))
	return nil
}
