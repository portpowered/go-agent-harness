//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the only composition boundary for the live delegation
// service.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation/internal/service"
)

// NewService assembles the live delegation executor service behind its
// public contract.
func NewService(deps Dependencies) livedelegation.Service {
	wire.Build(wire.FieldsOf(new(Dependencies), "Providers", "Credentials", "Scheduler", "Logger"), service.New, wire.Bind(new(livedelegation.Service), new(*service.Service)))
	return nil
}
