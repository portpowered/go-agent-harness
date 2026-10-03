package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Dependencies supplies host-owned infrastructure at the composition
// boundary: the provider service that builds each session's backend, the
// resolver for a backend's opaque credential reference, and the default
// clock for time budgets.
type Dependencies struct {
	Providers   providers.Service
	Credentials livedelegation.CredentialResolver
	Scheduler   clock.Scheduler
}
