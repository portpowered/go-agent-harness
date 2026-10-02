package observations

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// NewRuntimeTrace constructs an inert observer for one live invocation.
func NewRuntimeTrace(observer sessiontrace.RuntimeObserver, clock session.LiveClock, tick func() uint64) *RuntimeTrace {
	return NewInvocationTrace(observer, nil, clock, tick)
}
