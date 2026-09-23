// Package wire exposes the service-owned audio composition boundary.
package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/internal/service"
)

func NewService() audioio.Service { return service.New() }
