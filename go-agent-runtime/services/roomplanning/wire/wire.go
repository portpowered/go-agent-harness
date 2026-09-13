//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning/internal/service"
)

type Dependencies struct{}

func NewService(_ Dependencies) roomplanning.Service {
	wire.Build(service.New)
	return nil
}
