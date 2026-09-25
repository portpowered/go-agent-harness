package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/internal/fileports"
)

// NewFileMediaService assembles finite file and stdin media admission. It is
// stateless; each OpenFileMedia call owns the ports it opens until the
// returned handle is closed.
func NewFileMediaService() devices.FileMediaService {
	return fileports.New()
}
