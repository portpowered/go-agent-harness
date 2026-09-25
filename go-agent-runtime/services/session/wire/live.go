// Package wire contains the session service's explicit host composition.
package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/providerbuild"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// LiveDependencies describes the provider, tool, and evidence edges for a
// continuous session. Hosts own provider selection and replay construction;
// this package only assembles the private live implementation.
type LiveDependencies struct {
	InferencerFactory session.LiveInferencerFactory
	CapabilityFactory session.LiveCapabilityFactory
	ToolExecutor      messages.ToolExecutor
	ToolDefinitions   []messages.ToolDefinition
	EventCapacity     int
	Clock             session.LiveClock
	Scheduler         platformclock.Scheduler
	RuntimeObserver   sessiontrace.RuntimeObserver
	Tick              func() uint64
}

// NewLiveService assembles the continuous session role. It does not connect a
// provider until the returned handle's Start method is called.
func NewLiveService(deps LiveDependencies) session.LiveService {
	return live.New(live.Dependencies{
		InferencerFactory: deps.InferencerFactory,
		CapabilityFactory: deps.CapabilityFactory,
		ToolExecutor:      deps.ToolExecutor,
		ToolDefinitions:   append([]messages.ToolDefinition(nil), deps.ToolDefinitions...),
		EventCapacity:     deps.EventCapacity,
		Clock:             deps.Clock,
		Scheduler:         deps.Scheduler,
		RuntimeObserver:   deps.RuntimeObserver,
		Tick:              deps.Tick,
	})
}

// ProviderInferenceDependencies are the host edges for provider-backed live
// sessions: the provider service, the resolver for opaque credential
// references, an optional transport override, and the default tool surface
// advertised when a request carries no participant capabilities.
type ProviderInferenceDependencies struct {
	Providers       providers.SessionService
	Credentials     session.LiveCredentialResolver
	Dialer          transport.Dialer
	ToolDefinitions []messages.ToolDefinition
}

// NewProviderInferencerFactory assembles the session-owned projection from a
// live request to a provider session. Hosts keep secret storage and test
// seams; provider configuration translation stays in the session service.
func NewProviderInferencerFactory(deps ProviderInferenceDependencies) session.LiveInferencerFactory {
	return providerbuild.NewFactory(providerbuild.Dependencies{
		Providers: deps.Providers, Credentials: deps.Credentials,
		Dialer: deps.Dialer, ToolDefinitions: deps.ToolDefinitions,
	})
}
