//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the runtime-owned self-play implementation.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	selfplayinternal "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay/internal"
)

// NewService assembles self-play from explicit provider, catalog, and clock
// roles and returns only the public contract.
func NewService(deps Dependencies) selfplay.Service {
	wire.Build(
		wire.FieldsOf(new(Dependencies), "SessionService", "ModelCatalog", "Clock"),
		selfplayinternal.NewService,
		wire.Bind(new(selfplay.Service), new(*selfplayinternal.Service)),
	)
	return nil
}
