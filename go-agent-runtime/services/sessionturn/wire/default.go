//go:build !wireinject

package wire

import (
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	toolswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func NewDefaultService() sessionturn.Service {
	return NewService(Dependencies{
		PolicyFactory:      toolswire.NewInteractiveToolPolicy(),
		ImageStaging:       toolswire.NewImageStaging(),
		ToolService:        toolswire.NewService(),
		InstructionService: sessionwire.NewInstructionService(),
		LifecycleFactory:   wire.NewLifecycleService,
	})
}
