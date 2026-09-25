//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the session-turn
// service. Hosts supply the peer runtime capabilities it depends on.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/service"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// NewService assembles the session-turn implementation behind its public
// contract. Each service allocates its own text-seed sentinels.
func NewService(instructions session.InstructionService, policies tools.InteractiveToolPolicyFactory, staging tools.ImageStaging) sessionturn.Service {
	wire.Build(service.New, wire.Bind(new(sessionturn.Service), new(*service.Service)))
	return nil
}
