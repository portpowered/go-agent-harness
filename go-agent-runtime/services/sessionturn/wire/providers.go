//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session-turn implementation behind its public
// contract. Hosts may inject only the service's neutral allocation port.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn/internal/service"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

type Dependencies struct {
	Allocator          sessionturn.Allocator
	PolicyFactory      tools.InteractiveToolPolicyFactory
	ImageStaging       tools.ImageStaging
	ToolService        tools.Service
	InstructionService session.InstructionService
	LifecycleFactory   func() sessiontrace.LifecycleService
}

func NewService(deps Dependencies) sessionturn.Service {
	wire.Build(newServiceDependencies, service.New, wire.Bind(new(sessionturn.Service), new(*service.Service)))
	return nil
}

func newServiceDependencies(deps Dependencies) service.Dependencies {
	return service.Dependencies{Allocator: deps.Allocator, PolicyFactory: deps.PolicyFactory, ImageStaging: deps.ImageStaging, ToolService: deps.ToolService, InstructionService: deps.InstructionService, LifecycleFactory: deps.LifecycleFactory}
}
