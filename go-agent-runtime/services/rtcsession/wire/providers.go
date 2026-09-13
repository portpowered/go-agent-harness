//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the RTC session service without exposing its private
// lifecycle owner to embedding callers.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

// NewService creates an inert RTC lifecycle service. Endpoint resolution,
// data-plane creation, and media opening begin only when a runtime starts.
func NewService(components rtcsession.SessionRTCComponents, sampler observability.MetricSampler, logger observability.Logger) rtcsession.Service {
	wire.Build(service.New, wire.Bind(new(rtcsession.Service), new(*service.Service)))
	return nil
}
