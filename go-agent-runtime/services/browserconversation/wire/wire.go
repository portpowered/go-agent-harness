// Package wire composes the browser-scenario service for embedders.
package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation/internal/service"
)

// NewService returns a fresh, stateless browser-scenario service.
func NewService() browserconversation.Service { return service.New() }
