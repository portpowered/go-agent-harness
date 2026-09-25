// Package sessionturn owns the interactive session tool-execution policy:
// per-call latency budgets, panic isolation, correlated failure results, and
// operator diagnostics for one provider tool call. Hosts supply the resolved
// presentation ports; the decisions stay behind this contract and its private
// implementation. The live session runtime owns everything around the call:
// the advertised-surface allowlist, capture ordering, provider liveness,
// spoken acknowledgement, and dynamic tool publication.
package sessionturn

// Error is the stable identity type for session-turn sentinel errors. Values
// are constants so their identity cannot be reassigned at runtime.
type Error string

func (e Error) Error() string { return string(e) }

// ErrToolTimeout is retained behind a correlated tool result so callers can
// classify a local deadline without parsing the response content.
const ErrToolTimeout Error = "tool execution timed out"

// Service is the complete session tool-execution capability. Its
// implementation is private and constructed through services/sessionturn/wire.
type Service interface {
	ToolService
}
