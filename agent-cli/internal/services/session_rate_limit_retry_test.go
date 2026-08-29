package services

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRateLimitRetryDecision(t *testing.T) {
	tests := []struct {
		name           string
		status         string
		code           string
		message        string
		statusDetails  string
		terminalReason messages.TerminalReason
		wantDelay      time.Duration
		wantEligible   bool
	}{
		{
			name:         "positive decimal",
			status:       " FAILED ",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1.668s.",
			wantDelay:    1668 * time.Millisecond,
			wantEligible: true,
		},
		{
			name:         "provider message can contain context",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Request rate limit reached. Please try again in 0.25s.",
			wantDelay:    250 * time.Millisecond,
			wantEligible: true,
		},
		{
			name:         "leading decimal",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in .5s",
			wantDelay:    500 * time.Millisecond,
			wantEligible: true,
		},
		{
			name:         "cap",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in 45s",
			wantDelay:    maxRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "missing message fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "malformed message fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please retry after a short while",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "zero fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in 0s",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "negative fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in -1s",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "non finite fallback",
			status:       "failed",
			code:         rateLimitRetryCode,
			message:      "Please try again in NaNs",
			wantDelay:    defaultRateLimitRetryDelay,
			wantEligible: true,
		},
		{
			name:         "incomplete is not eligible",
			status:       "incomplete",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:         "cancelled is not eligible",
			status:       "cancelled",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:           "cancellation reason is not eligible",
			status:         "failed",
			code:           rateLimitRetryCode,
			message:        "Please try again in 1s",
			terminalReason: messages.TerminalReasonCancellation,
			wantEligible:   false,
		},
		{
			name:         "completed is not eligible",
			status:       "completed",
			code:         rateLimitRetryCode,
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:         "substring code is not eligible",
			status:       "failed",
			code:         "quota_rate_limit_exceeded",
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:         "case changed code is not eligible",
			status:       "failed",
			code:         "RATE_LIMIT_EXCEEDED",
			message:      "Please try again in 1s",
			wantEligible: false,
		},
		{
			name:          "legacy compact details",
			status:        "failed",
			statusDetails: "reason=error, code=rate_limit_exceeded, message=Please try again in 1.5s.",
			wantDelay:     1500 * time.Millisecond,
			wantEligible:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := &messages.MessageEndValue{
				Type:                 "message_end",
				Status:               test.status,
				StatusDetails:        test.statusDetails,
				ProviderErrorCode:    test.code,
				ProviderErrorMessage: test.message,
				TerminalReason:       test.terminalReason,
			}
			gotDelay, gotEligible := rateLimitRetryDecision(terminal)
			if gotEligible != test.wantEligible {
				t.Fatalf("eligible = %t, want %t", gotEligible, test.wantEligible)
			}
			if gotEligible && gotDelay != test.wantDelay {
				t.Fatalf("delay = %s, want %s", gotDelay, test.wantDelay)
			}
			if !gotEligible && gotDelay != 0 {
				t.Fatalf("ineligible delay = %s, want zero", gotDelay)
			}
		})
	}
}

func TestRateLimitRetryDecisionNilTerminalIsIneligible(t *testing.T) {
	if delay, eligible := rateLimitRetryDecision(nil); eligible || delay != 0 {
		t.Fatalf("nil terminal decision = (%s, %t), want (0, false)", delay, eligible)
	}
}
