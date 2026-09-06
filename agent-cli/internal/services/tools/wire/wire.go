// Package wire exposes the CLI tools service's narrow composition constructors.
// The implementation remains below the service-local internal boundary.
package wire

import (
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	runtimeadapter "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools/internal/runtimeadapter"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// NewRuntimeToolServiceAdapter bridges a host-resolved CLI capability service
// into the reusable runtime contract at the application composition edge.
func NewRuntimeToolServiceAdapter(host serviceTools.Service, fallback runtimeTools.Service) runtimeTools.Service {
	return runtimeadapter.New(host, fallback)
}
