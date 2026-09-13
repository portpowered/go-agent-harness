//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the session-observation service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewService creates an isolated recorder with the supplied observer and
// clock. No provider, CLI, credential, or process-global dependency is read.
func NewService(observer sessionobservation.SessionRuntimeObserver, source clock.Source) sessionobservation.Service {
	wire.Build(service.New, wire.Bind(new(sessionobservation.Service), new(*service.Service)))
	return nil
}
