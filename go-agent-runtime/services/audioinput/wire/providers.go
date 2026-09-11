//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire composes the audio-input service without exposing its private
// stream implementation to hosts or embedders.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput/internal/stream"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewService creates an inert service. The caller supplies the clock used by
// paced sources when a request does not provide a per-input clock.
func NewService(source clock.Source) audioinput.Service {
	wire.Build(stream.New, wire.Bind(new(audioinput.Service), new(*stream.Service)))
	return nil
}
