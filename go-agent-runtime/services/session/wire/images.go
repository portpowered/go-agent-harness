package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/imagestage"
)

// NewLiveImageStager assembles the stateless opening-image staging role used
// before a live request is admitted.
func NewLiveImageStager() session.LiveImageStager {
	return imagestage.New()
}
