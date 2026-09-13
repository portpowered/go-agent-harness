// Package service keeps the private provider path stable for the dedicated
// sessioninstructions Wire graph. The single policy implementation is
// unexported in the public contract package; this wrapper only exposes its
// stateless construction to Wire.
package service

import sessioninstructions "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"

// New returns the canonical stateless instruction service.
func New() sessioninstructions.InstructionService {
	return sessioninstructions.Factory{}.Build()
}
