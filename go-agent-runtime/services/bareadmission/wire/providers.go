//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for bare-session admission.
// The generated graph returns the public service while keeping policy private.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/bareadmission/internal/service"
)

func NewService() bareadmission.Service {
	wire.Build(service.New, wire.Bind(new(bareadmission.Service), new(*service.Service)))
	return nil
}
