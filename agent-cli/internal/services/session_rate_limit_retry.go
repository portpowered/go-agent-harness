package services

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	rateLimitRetryCode         = "rate_limit_exceeded"
	defaultRateLimitRetryDelay = 2 * time.Second
	maxRateLimitRetryDelay     = 15 * time.Second
	maxLegacyStatusDetailBytes = 256
)

var rateLimitRetryDelayPattern = regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`)

// rateLimitRetryDecision classifies one provider response terminal and, when
// eligible, returns the bounded delay requested by the provider. The session
// runtime owns the resulting retry policy; this helper only interprets the
// provider-neutral terminal metadata.
func rateLimitRetryDecision(terminal *messages.MessageEndValue) (time.Duration, bool) {
	if terminal == nil || normalizeTerminalStatus(terminal.Status) != "failed" || terminal.TerminalReason == messages.TerminalReasonCancellation {
		return 0, false
	}
	if providerTerminalErrorCode(terminal) != rateLimitRetryCode {
		return 0, false
	}
	return parseRateLimitRetryDelay(providerTerminalErrorMessage(terminal)), true
}

func parseRateLimitRetryDelay(message string) time.Duration {
	match := rateLimitRetryDelayPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return defaultRateLimitRetryDelay
	}

	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return defaultRateLimitRetryDelay
	}
	if seconds > maxRateLimitRetryDelay.Seconds() {
		return maxRateLimitRetryDelay
	}

	// Round to the nearest representable nanosecond and retain a positive
	// duration for a valid value that is smaller than one nanosecond.
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}

func providerTerminalErrorCode(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if code := strings.TrimSpace(terminal.ProviderErrorCode); code != "" {
		return code
	}
	return legacyStatusDetailField(terminal.StatusDetails, "code")
}

func providerTerminalErrorMessage(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if message := strings.TrimSpace(terminal.ProviderErrorMessage); message != "" {
		return message
	}
	return legacyStatusDetailField(terminal.StatusDetails, "message")
}

func legacyStatusDetailField(details, wanted string) string {
	parts := strings.Split(details, ",")
	for index, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(key) != wanted {
			continue
		}
		value = strings.TrimSpace(value)
		if wanted == "message" && index+1 < len(parts) {
			// The compact legacy representation places message last, but the
			// provider text itself may contain commas. Rejoin the remainder so
			// retry guidance is not lost when the explicit field is unavailable.
			value = strings.TrimSpace(strings.Join(append([]string{value}, parts[index+1:]...), ","))
		}
		if len(value) > maxLegacyStatusDetailBytes {
			value = value[:maxLegacyStatusDetailBytes]
		}
		return value
	}
	return ""
}

func normalizeTerminalStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}
