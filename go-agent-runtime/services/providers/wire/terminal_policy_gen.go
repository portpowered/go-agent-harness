//go:build !wireinject
// +build !wireinject

package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/terminalpolicy"
)

// NewTerminalPolicy is the default implementation of the dedicated
// terminal-policy injector declared in terminal_policy.go. It is kept in its
// own file so the existing full provider graph's generated wire_gen.go stays
// unchanged.
func NewTerminalPolicy() providers.TerminalPolicy {
	policy := terminalpolicy.New()
	return policy
}
