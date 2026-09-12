//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session configuration service while keeping its
// implementation package private to the composition boundary.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionconfig/internal/service"
)

// Dependencies supplies the immutable provider model catalog used for model
// capability admission. Hosts may inject a custom catalog for deterministic
// tests and independent consumers.
type Dependencies struct {
	ModelCatalog providers.ModelCatalog
}

// NewService assembles the public session configuration contract.
func NewService(deps Dependencies) sessionconfig.Service {
	wire.Build(wire.FieldsOf(new(Dependencies), "ModelCatalog"), service.New, wire.Bind(new(sessionconfig.Service), new(*service.Service)))
	return nil
}
