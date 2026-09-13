//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/internal/service"
)

// NewService assembles one stateless terminal policy service.
func NewService() sessionterminal.Service {
	wire.Build(service.New, wire.Bind(new(sessionterminal.Service), new(*service.Service)))
	return nil
}
