package wire

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"

// NewService composes the production room evidence service: zero options
// keep every production default, including an fsync for each artifact.
func NewService() roomevidence.Service {
	return NewServiceWithOptions(roomevidence.ServiceOptions{})
}
