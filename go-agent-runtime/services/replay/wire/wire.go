//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan"
)

// NewService assembles the replay planning service without opening artifacts.
func NewService() replay.Service {
	wire.Build(plan.New, wire.Bind(new(replay.Service), new(*plan.Service)))
	return nil
}
