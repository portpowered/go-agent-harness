// Package wire composes the browser-scenario service for embedders.
package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario/internal/service"
)

// NewService returns a fresh, stateless browser-scenario service.
func NewService() browserscenario.Service { return service.New() }
