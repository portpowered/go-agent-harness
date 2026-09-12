//go:build wireinject
// +build wireinject

//go:generate go run -mod=mod github.com/google/wire/cmd/wire

// Package wire is the dedicated composition boundary for the retry policy.
package wire

import (
	"github.com/google/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy/internal/service"
)

// NewService returns a fresh stateless retry-policy service.
func NewService() retrypolicy.Service {
	wire.Build(service.New, wire.Bind(new(retrypolicy.Service), new(*service.Service)))
	return nil
}
