// Package service contains the private browserrunner implementations.
package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"

// NewEvidenceTracker returns the private implementation through the public
// tracker contract.
func NewEvidenceTracker(config browserrunner.EvidenceTrackerConfig) browserrunner.EvidenceTracker {
	return newEvidenceTracker(config)
}

// NewInterruptionController returns the private implementation through the
// public interruption contract.
func NewInterruptionController(config browserrunner.InterruptionControllerConfig) browserrunner.InterruptionController {
	return newInterruptionController(config)
}
