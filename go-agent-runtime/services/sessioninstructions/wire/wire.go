//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the composition boundary for session instruction policy.
// Hosts and embedders receive only the public contract; the implementation
// remains private to this service graph.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions/internal/service"
)

// NewInstructionService assembles the stateless instruction policy service.
func NewInstructionService() sessioninstructions.InstructionService {
	wire.Build(service.New)
	return nil
}
