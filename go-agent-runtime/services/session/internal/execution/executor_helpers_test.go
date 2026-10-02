package agent

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// NewExecutor creates a new Executor with the given dependencies.
func NewExecutor(executor messages.ToolExecutor, toolDefs []messages.ToolDefinition, inferencerOverride messages.Inferencer, relaxModelValidation ...bool) *Executor {
	return newExecutor(nil, executor, toolDefs, inferencerOverride, nil, relaxModelValidation...)
}
