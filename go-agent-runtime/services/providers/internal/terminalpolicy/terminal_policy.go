package terminalpolicy

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	providers "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

const rateLimitRetryDelayPattern = `(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`

// Policy is the stateless provider terminal-policy implementation. The
// compiled expression is owned by this service instance rather than a package
// singleton so construction remains explicit and request-independent.
type Policy struct {
	delayPattern *regexp.Regexp
}

// New constructs one immutable terminal policy service.
func New() *Policy {
	return &Policy{delayPattern: regexp.MustCompile(rateLimitRetryDelayPattern)}
}

// RateLimitRetryDecision classifies one provider terminal and, when eligible,
// returns the bounded delay requested by the provider. Session scheduling and
// lifecycle ownership remain with the caller.
func (p *Policy) RateLimitRetryDecision(terminal *messages.MessageEndValue) (time.Duration, bool) {
	if terminal == nil || p.NormalizeTerminalStatus(terminal.Status) != "failed" || terminal.TerminalReason == messages.TerminalReasonCancellation {
		return 0, false
	}
	if p.ProviderTerminalErrorCode(terminal) != providers.RateLimitRetryCode {
		return 0, false
	}
	return p.ParseRateLimitRetryDelay(p.ProviderTerminalErrorMessage(terminal)), true
}

// ParseRateLimitRetryDelay extracts and bounds the provider's decimal seconds
// hint, retaining the historical default, cap, rounding, and minimum rules.
func (p *Policy) ParseRateLimitRetryDelay(message string) time.Duration {
	match := p.retryDelayPattern().FindStringSubmatch(message)
	if len(match) != 2 {
		return providers.DefaultRateLimitRetryDelay
	}

	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return providers.DefaultRateLimitRetryDelay
	}
	if seconds > providers.MaxRateLimitRetryDelay.Seconds() {
		return providers.MaxRateLimitRetryDelay
	}

	// Round to the nearest representable nanosecond and retain a positive
	// duration for a valid value that is smaller than one nanosecond.
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}

// ProviderTerminalErrorCode prefers explicit provider metadata over the
// compact legacy representation.
func (p *Policy) ProviderTerminalErrorCode(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if code := strings.TrimSpace(terminal.ProviderErrorCode); code != "" {
		return code
	}
	return p.LegacyStatusDetailField(terminal.StatusDetails, "code")
}

// ProviderTerminalErrorMessage prefers explicit provider metadata over the
// compact legacy representation.
func (p *Policy) ProviderTerminalErrorMessage(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if message := strings.TrimSpace(terminal.ProviderErrorMessage); message != "" {
		return message
	}
	return p.LegacyStatusDetailField(terminal.StatusDetails, "message")
}

// LegacyStatusDetailField extracts one bounded field from the compact
// comma-separated status-details representation. Message values may contain
// commas and therefore consume the remainder of the representation.
func (p *Policy) LegacyStatusDetailField(details, wanted string) string {
	parts := strings.Split(details, ",")
	for index, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) != wanted {
			continue
		}
		value = strings.TrimSpace(value)
		if wanted == "message" && index+1 < len(parts) {
			value = strings.TrimSpace(strings.Join(append([]string{value}, parts[index+1:]...), ","))
		}
		if len(value) > providers.MaxLegacyStatusDetailBytes {
			value = value[:providers.MaxLegacyStatusDetailBytes]
		}
		return value
	}
	return ""
}

// NormalizeTerminalStatus retains the provider status normalization used by
// the CLI compatibility surface.
func (p *Policy) NormalizeTerminalStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func (p *Policy) retryDelayPattern() *regexp.Regexp {
	if p != nil && p.delayPattern != nil {
		return p.delayPattern
	}
	return regexp.MustCompile(rateLimitRetryDelayPattern)
}

var _ providers.TerminalPolicy = (*Policy)(nil)
