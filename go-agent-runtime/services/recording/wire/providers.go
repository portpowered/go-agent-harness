//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the recording service as a whole.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func NewService(source clock.Source) recording.Service {
	wire.Build(service.New, wire.Bind(new(recording.Service), new(*service.Service)))
	return nil
}

func NewProviderCaptureService(source clock.Source) recording.ProviderCaptureService {
	wire.Build(service.New, wire.Bind(new(recording.ProviderCaptureService), new(*service.Service)))
	return nil
}
