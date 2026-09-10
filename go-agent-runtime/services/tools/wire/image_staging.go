package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/imagestaging"
)

// NewImageStaging composes the private image-staging implementation behind
// the public tools contract. It is independent of the generated default tool
// graph so hosts can stage images without importing CLI or filesystem internals.
func NewImageStaging() tools.ImageStaging { return imagestaging.New() }
