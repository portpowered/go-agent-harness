//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the text-seed runtime service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed/internal/service"
)

// NewService constructs a service with an explicit allocator. Passing nil
// selects the constructor-local production allocator.
func NewService(allocator textseed.Allocator) textseed.Service {
	wire.Build(service.New, wire.Bind(new(textseed.Service), new(*service.Service)))
	return nil
}

// NewDefaultService constructs a service with a fresh service-owned allocator.
func NewDefaultService() textseed.Service {
	wire.Build(service.NewDefault, wire.Bind(new(textseed.Service), new(*service.Service)))
	return nil
}
