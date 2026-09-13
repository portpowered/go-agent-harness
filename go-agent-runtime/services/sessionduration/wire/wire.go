//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/internal/service"
)

// NewService assembles the terminal-boundary implementation behind its public
// host-neutral contract.
func NewService() sessionduration.Service {
	wire.Build(service.New, wire.Bind(new(sessionduration.Service), new(*service.Service)))
	return nil
}
