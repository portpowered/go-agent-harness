// Package service contains the private browserrunner implementations.
package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"

type service struct{}

// NewService returns the private browserrunner composition service through
// the public contract. The service is stateless; every returned child owns its
// own mutable tracker/controller state.
func NewService() browserrunner.Service {
	return &service{}
}

func (s *service) NewEvidenceTracker(config browserrunner.EvidenceTrackerConfig) browserrunner.EvidenceTracker {
	return newEvidenceTracker(config)
}

func (s *service) NewInterruptionController(config browserrunner.InterruptionControllerConfig) browserrunner.InterruptionController {
	return newInterruptionController(config)
}

func (s *service) PartitionAudioInputs(steps []browserrunner.StepBoundary, inputs []browserrunner.AudioInput) ([]browserrunner.AudioInput, map[string]browserrunner.AudioInput) {
	return partitionAudioInputs(steps, inputs)
}

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
