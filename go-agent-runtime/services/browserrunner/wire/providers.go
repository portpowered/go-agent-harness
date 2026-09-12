//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the browserrunner
// service. The generated graph returns public contracts while keeping the
// tracker and interruption implementation private to the runtime module.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserrunner/internal/service"
)

// NewEvidenceTracker constructs the provider-neutral evidence tracker.
func NewEvidenceTracker(config browserrunner.EvidenceTrackerConfig) browserrunner.EvidenceTracker {
	wire.Build(service.NewEvidenceTracker)
	return nil
}

// NewInterruptionController constructs the bounded event-driven audio
// interruption controller.
func NewInterruptionController(config browserrunner.InterruptionControllerConfig) browserrunner.InterruptionController {
	wire.Build(service.NewInterruptionController)
	return nil
}
