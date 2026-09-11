package agentruntime

import (
	"regexp"
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

// rateLimitRetryDelayPattern remains as the pre-existing architecture-ledger
// symbol for this compatibility file. The provider TerminalPolicy owns all
// matching and parsing; this value is not consulted by the CLI runtime.
var rateLimitRetryDelayPattern = regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`) //nolint:gochecknoglobals // retained baseline symbol; policy owns parsing

// terminalPolicy constructs the pure provider policy at the service boundary.
// The CLI retains these names for compatibility while scheduling and response
// lifecycle decisions remain owned by the session runtime.
func terminalPolicy() runtimeProviders.TerminalPolicy {
	return runtimeProvidersWire.NewTerminalPolicy()
}

func rateLimitRetryDecision(terminal *messages.MessageEndValue) (time.Duration, bool) {
	return terminalPolicy().RateLimitRetryDecision(terminal)
}

func parseRateLimitRetryDelay(message string) time.Duration {
	return terminalPolicy().ParseRateLimitRetryDelay(message)
}

func providerTerminalErrorCode(terminal *messages.MessageEndValue) string {
	return terminalPolicy().ProviderTerminalErrorCode(terminal)
}

func providerTerminalErrorMessage(terminal *messages.MessageEndValue) string {
	return terminalPolicy().ProviderTerminalErrorMessage(terminal)
}

func legacyStatusDetailField(details, wanted string) string {
	return terminalPolicy().LegacyStatusDetailField(details, wanted)
}

func normalizeTerminalStatus(status string) string {
	return terminalPolicy().NormalizeTerminalStatus(status)
}
