package gateway

import "github.com/portpowered/go-llm-gateway/pkg/providers"

var _ CapabilityReporter = (*DefaultGateway)(nil)
var _ CapabilityReporter = (*DefaultSessionGateway)(nil)

type namedCapabilityProvider interface {
	Name() string
}

func providerCapabilities(provider namedCapabilityProvider) ProviderCapabilities {
	if reporter, ok := provider.(providers.CapabilityReporter); ok {
		caps := reporter.Capabilities()
		if caps.Provider == "" {
			caps.Provider = provider.Name()
		}
		return caps
	}
	return providers.UnknownProviderCapabilities(provider.Name())
}
