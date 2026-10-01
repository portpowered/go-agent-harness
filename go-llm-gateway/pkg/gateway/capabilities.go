package gateway

import "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"

var _ Gateway = (*DefaultGateway)(nil)
var _ CapabilityReporter = (*DefaultGateway)(nil)
var _ CapabilityReporter = (*DefaultSessionGateway)(nil)

type namedCapabilityProvider interface {
	Name() string
}

func providerCapabilities(provider namedCapabilityProvider) ProviderCapabilities {
	return providers.ReportedCapabilities(provider)
}
