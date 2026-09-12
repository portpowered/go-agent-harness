// Package execution owns the private bounded invocation controller used by
// the reusable tools service.
package execution

import public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"

// Factory constructs request-scoped controllers. It carries no invocation
// state and is safe to share through the service's Wire graph.
type Factory struct{}

func New() *Factory { return &Factory{} }

func (f *Factory) NewToolExecutionController(request public.ToolExecutionRequest) public.ToolExecutionController {
	return newController(request)
}

var _ public.ExecutionService = (*Factory)(nil)
