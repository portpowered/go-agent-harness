// Package service preserves the private implementation package used by the
// dedicated sessioninstructions Wire graph. The implementation itself lives
// behind the public contract's stateless compatibility bridge so other
// service composition packages do not reach across a peer Wire boundary.
package service

import sessioninstructions "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"

// New returns the private, stateless instruction service for the dedicated
// sessioninstructions composition root.
func New() sessioninstructions.InstructionService {
	return sessioninstructions.Factory{}.Build()
}
