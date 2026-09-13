package wire

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioncontinuation"

// New is a concise composition alias for the generated constructor.
func New() sessioncontinuation.Service { return NewService() }
