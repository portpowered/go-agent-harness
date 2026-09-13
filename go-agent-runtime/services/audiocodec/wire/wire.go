//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire assembles the process-backed audiocodec service while keeping
// its filesystem and process implementation private to the service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiocodec/internal/service"
)

// NewService constructs one independent codec service. It performs no process
// lookup or filesystem access until Convert is called.
func NewService() audiocodec.Service {
	wire.Build(service.New, wire.Bind(new(audiocodec.Service), new(*service.Service)))
	return nil
}
