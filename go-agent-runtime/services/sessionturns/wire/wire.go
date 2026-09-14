//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the session-turn service behind its public contract.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns/internal/service"
)

// Dependencies are the explicit host-neutral edges used to construct one
// isolated session-turn service.
type Dependencies struct {
	SessionInferencer messages.SessionInferencer
	EventSink         sessionturns.TurnEventSink
}

// NewService constructs an inert service. It does not connect a provider.
func NewService(deps Dependencies) sessionturns.Service {
	wire.Build(newService, wire.Bind(new(sessionturns.Service), new(*service.Service)))
	return nil
}

func newService(deps Dependencies) *service.Service {
	return service.New(service.Dependencies{
		SessionInferencer: deps.SessionInferencer,
		EventSink:         deps.EventSink,
	})
}
