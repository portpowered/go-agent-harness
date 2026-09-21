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
