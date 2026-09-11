package providers

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// RateLimitRetryCode is the exact provider error code eligible for a
	// session-owned retry.
	RateLimitRetryCode = "rate_limit_exceeded"
	// DefaultRateLimitRetryDelay is used when a provider does not include a
	// valid retry delay.
	DefaultRateLimitRetryDelay = 2 * time.Second
	// MaxRateLimitRetryDelay bounds a provider-requested retry delay.
	MaxRateLimitRetryDelay = 15 * time.Second
	// MaxLegacyStatusDetailBytes bounds values recovered from the compact
	// legacy status-details representation.
	MaxLegacyStatusDetailBytes = 256
)

// TerminalPolicy owns provider-neutral terminal classification and retry-delay
// interpretation. The implementation is private to the providers service;
// hosts receive it through the service's Wire boundary.
type TerminalPolicy interface {
	RateLimitRetryDecision(*messages.MessageEndValue) (time.Duration, bool)
	ParseRateLimitRetryDelay(string) time.Duration
	ProviderTerminalErrorCode(*messages.MessageEndValue) string
	ProviderTerminalErrorMessage(*messages.MessageEndValue) string
	LegacyStatusDetailField(details, wanted string) string
	NormalizeTerminalStatus(string) string
}
