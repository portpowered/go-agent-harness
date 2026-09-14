//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule/internal/service"
)

// NewService assembles the credential-free room replay scheduling service.
func NewService() roomreplayschedule.Service {
	wire.Build(service.New, wire.Bind(new(roomreplayschedule.Service), new(*service.Service)))
	return nil
}
