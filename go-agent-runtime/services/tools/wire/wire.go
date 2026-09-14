//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the tools service. The
// generated graph returns the public service contract while keeping the
// registry implementation private to this service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/execution"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/policy"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/service"
)

// NewService creates the inert tools service. Tool resources are resolved per
// request by the returned service rather than during graph construction.
func NewService() tools.Service {
	wire.Build(execution.New, service.NewWithExecution, wire.Bind(new(tools.Service), new(*service.Service)))
	return nil
}

// NewExecutionService exposes the optional controller capability through the
// same registered tools service graph.
func NewExecutionService() tools.ExecutionService {
	wire.Build(execution.New, service.NewWithExecution, wire.Bind(new(tools.ExecutionService), new(*service.Service)))
	return nil
}

// NewInteractiveToolPolicy creates the reusable interactive-policy factory.
func NewInteractiveToolPolicy() tools.InteractiveToolPolicyFactory {
	wire.Build(policy.New, wire.Bind(new(tools.InteractiveToolPolicyFactory), new(*policy.Factory)))
	return nil
}
