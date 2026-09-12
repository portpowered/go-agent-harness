// Package service contains the private retry-policy implementation.
package service

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy"
)

const (
	rateLimitExceededCode  = "rate_limit_exceeded"
	defaultRetryDelay      = 2 * time.Second
	maximumRetryDelay      = 15 * time.Second
	maximumLegacyDetailLen = 256
	cancellationReason     = "cancellation"
)

// Service implements the stateless provider-neutral retry policy.
type Service struct {
	retryDelayPattern *regexp.Regexp
}

var _ retrypolicy.Service = (*Service)(nil)

// New returns an inert retry-policy implementation.
func New() *Service {
	return &Service{retryDelayPattern: regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`)}
}

// Decide classifies a terminal and returns its bounded retry delay.
func (s *Service) Decide(terminal retrypolicy.Terminal) retrypolicy.Decision {
	if s.NormalizeStatus(terminal.Status) != "failed" || terminal.TerminalReason == cancellationReason {
		return retrypolicy.Decision{}
	}
	if s.ProviderErrorCode(terminal) != rateLimitExceededCode {
		return retrypolicy.Decision{}
	}
	return retrypolicy.Decision{Delay: s.parseRetryDelay(s.ProviderErrorMessage(terminal)), Eligible: true}
}

// ProviderErrorCode applies explicit-field precedence before legacy parsing.
func (*Service) ProviderErrorCode(terminal retrypolicy.Terminal) string {
	if value := strings.TrimSpace(terminal.ProviderErrorCode); value != "" {
		return value
	}
	return legacyStatusDetailField(terminal.StatusDetails, "code")
}

// ProviderErrorMessage applies explicit-field precedence before legacy parsing.
func (*Service) ProviderErrorMessage(terminal retrypolicy.Terminal) string {
	if value := strings.TrimSpace(terminal.ProviderErrorMessage); value != "" {
		return value
	}
	return legacyStatusDetailField(terminal.StatusDetails, "message")
}

// NormalizeStatus keeps the legacy status comparison provider-neutral.
func (*Service) NormalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func legacyStatusDetailField(details, wanted string) string {
	if len(details) > maximumLegacyDetailLen {
		details = details[:maximumLegacyDetailLen]
	}
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
		if len(value) > maximumLegacyDetailLen {
			value = value[:maximumLegacyDetailLen]
		}
		return value
	}
	return ""
}

func (s *Service) parseRetryDelay(message string) time.Duration {
	match := s.retryDelayPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return defaultRetryDelay
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return defaultRetryDelay
	}
	if seconds > maximumRetryDelay.Seconds() {
		return maximumRetryDelay
	}
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}
