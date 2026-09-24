package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Dependencies supplies already-composed runtime roles. Constructing the
// self-play service stores them and does not open a provider session.
type Dependencies struct {
	SessionService providers.SessionService
	ModelCatalog   providers.ModelCatalog
	Clock          clock.Source
}

// NewDependencies maps the application-owned provider roles into the
// self-play service's own Wire boundary.
func NewDependencies(sessionService providers.SessionService, modelCatalog providers.ModelCatalog, source clock.Source) Dependencies {
	return Dependencies{SessionService: sessionService, ModelCatalog: modelCatalog, Clock: source}
}
