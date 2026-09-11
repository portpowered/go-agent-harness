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

// rateLimitRetryDelayPattern remains the pre-existing architecture-baseline
// symbol until the owning package retires that shared ledger entry. Parsing is
// provider-owned; this compatibility symbol is intentionally not consulted by
// the CLI runtime.
var rateLimitRetryDelayPattern = regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`) //nolint:gochecknoglobals // retained pre-existing baseline symbol

// terminalPolicy constructs the pure provider policy at the service boundary.
// The CLI retains these names for compatibility while scheduling and response
// lifecycle decisions remain owned by the session runtime.
func terminalPolicy() runtimeProviders.TerminalPolicy {
	// Keep the pre-existing baseline symbol live without reintroducing parsing
	// or a second eligibility/normalization branch in the CLI.
	_ = rateLimitRetryDelayPattern
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
