package agentruntime

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	runtimeProvidersWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
)

const (
	rateLimitRetryCode         = runtimeProviders.RateLimitRetryCode
	defaultRateLimitRetryDelay = runtimeProviders.DefaultRateLimitRetryDelay
	maxRateLimitRetryDelay     = runtimeProviders.MaxRateLimitRetryDelay
	maxLegacyStatusDetailBytes = runtimeProviders.MaxLegacyStatusDetailBytes
)

// terminalPolicy constructs the pure provider policy at the service boundary.
// The CLI retains these names for compatibility while scheduling and response
// lifecycle decisions remain owned by the session runtime.
func terminalPolicy() runtimeProviders.TerminalPolicy {
	return runtimeProvidersWire.NewTerminalPolicy()
}

func rateLimitRetryDecision(terminal *messages.MessageEndValue) (time.Duration, bool) {
	return terminalPolicy().RateLimitRetryDecision(terminal)
}

func providerTerminalErrorCode(terminal *messages.MessageEndValue) string {
	return terminalPolicy().ProviderTerminalErrorCode(terminal)
}

func providerTerminalErrorMessage(terminal *messages.MessageEndValue) string {
	return terminalPolicy().ProviderTerminalErrorMessage(terminal)
}

func normalizeTerminalStatus(status string) string {
	return terminalPolicy().NormalizeTerminalStatus(status)
}
