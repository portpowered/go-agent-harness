package composition

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// WithSessionImagePreparer returns a composition snapshot whose routed
// executors use the session-owned image preparation callback. Browser broker
// routes and executors without the optional binder are preserved unchanged;
// the local registry route is rebound without mutating another session's
// capability snapshot.
func (e *composedToolExecutor) WithSessionImagePreparer(preparer public.ImagePartPreparer) messages.ToolExecutor {
	if e == nil {
		return nil
	}
	routes := make(map[string]toolRoute, len(e.routes))
	for name, route := range e.routes {
		if binder, ok := route.executor.(public.SessionImagePreparerBinder); ok {
			route.executor = binder.WithSessionImagePreparer(preparer)
		}
		routes[name] = route
	}
	dynamicFallback := e.dynamicFallback
	if binder, ok := dynamicFallback.(public.SessionImagePreparerBinder); ok {
		dynamicFallback = binder.WithSessionImagePreparer(preparer)
	}
	return &composedToolExecutor{routes: routes, dynamicFallback: dynamicFallback}
}
