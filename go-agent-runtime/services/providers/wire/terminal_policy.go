//go:build wireinject && terminalpolicywire
// +build wireinject,terminalpolicywire

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/terminalpolicy"
)

// NewTerminalPolicy is the dedicated Wire injector for the provider terminal
// policy. The extra generation tag keeps this independent graph out of the
// existing full provider graph's generated file.
func NewTerminalPolicy() providers.TerminalPolicy {
	wire.Build(terminalpolicy.New, wire.Bind(new(providers.TerminalPolicy), new(*terminalpolicy.Policy)))
	return nil
}
