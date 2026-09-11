package terminalpolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	providers "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
)

func TestPolicyRateLimitRetryDecisionPreservesEligibility(t *testing.T) {
	policy := New()
	tests := []struct {
		name         string
		terminal     *messages.MessageEndValue
		wantDelay    time.Duration
		wantEligible bool
	}{
		{
			name:         "nil terminal",
			terminal:     nil,
			wantEligible: false,
		},
		{
			name: "normalized failed terminal",
			terminal: &messages.MessageEndValue{
				Status:               " FAILED ",
				ProviderErrorCode:    providers.RateLimitRetryCode,
				ProviderErrorMessage: "Please try again in 1.668s.",
			},
			wantDelay:    1668 * time.Millisecond,
			wantEligible: true,
		},
		{
			name: "legacy fields",
			terminal: &messages.MessageEndValue{
				Status:        "failed",
				StatusDetails: "reason=error, code=rate_limit_exceeded, message=Please try again in 1.5s.",
			},
			wantDelay:    1500 * time.Millisecond,
			wantEligible: true,
		},
		{
			name: "cancellation reason excluded",
			terminal: &messages.MessageEndValue{
				Status:               "failed",
				ProviderErrorCode:    providers.RateLimitRetryCode,
				ProviderErrorMessage: "Please try again in 1s",
				TerminalReason:       messages.TerminalReasonCancellation,
			},
			wantEligible: false,
		},
		{
			name: "non failed status excluded",
			terminal: &messages.MessageEndValue{
				Status:               "incomplete",
				ProviderErrorCode:    providers.RateLimitRetryCode,
				ProviderErrorMessage: "Please try again in 1s",
			},
			wantEligible: false,
		},
		{
			name: "code remains exact and case sensitive",
			terminal: &messages.MessageEndValue{
				Status:               "failed",
				ProviderErrorCode:    "RATE_LIMIT_EXCEEDED",
				ProviderErrorMessage: "Please try again in 1s",
			},
			wantEligible: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotDelay, gotEligible := policy.RateLimitRetryDecision(test.terminal)
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

func TestPolicyMetadataPrecedenceAndLegacyBounds(t *testing.T) {
	policy := New()
	legacyMessage := "Please try again in 1s, but preserve this provider context."
	if got := policy.ProviderTerminalErrorCode(&messages.MessageEndValue{
		ProviderErrorCode: " explicit-code ",
		StatusDetails:     "code=legacy-code",
	}); got != "explicit-code" {
		t.Fatalf("explicit code = %q, want explicit-code", got)
	}
	if got := policy.ProviderTerminalErrorMessage(&messages.MessageEndValue{
		ProviderErrorMessage: " explicit message ",
		StatusDetails:        "message=legacy message",
	}); got != "explicit message" {
		t.Fatalf("explicit message = %q, want explicit message", got)
	}
	terminal := &messages.MessageEndValue{StatusDetails: "reason=error, code=rate_limit_exceeded, message=" + legacyMessage}
	if got := policy.ProviderTerminalErrorCode(terminal); got != providers.RateLimitRetryCode {
		t.Fatalf("legacy code = %q, want %q", got, providers.RateLimitRetryCode)
	}
	if got := policy.ProviderTerminalErrorMessage(terminal); got != legacyMessage {
		t.Fatalf("legacy message = %q, want %q", got, legacyMessage)
	}
	long := strings.Repeat("x", providers.MaxLegacyStatusDetailBytes+17)
	if got := policy.LegacyStatusDetailField("message="+long, "message"); len(got) != providers.MaxLegacyStatusDetailBytes {
		t.Fatalf("legacy message length = %d, want %d", len(got), providers.MaxLegacyStatusDetailBytes)
	}
	if got := policy.LegacyStatusDetailField("malformed,other=value", "missing"); got != "" {
		t.Fatalf("missing legacy field = %q, want empty", got)
	}
}

func TestPolicyRetryDelayParsingRetainsDefaultsBoundsAndRounding(t *testing.T) {
	policy := New()
	tests := map[string]time.Duration{
		"Please try again in .5s":             500 * time.Millisecond,
		"Please try again in 45s":             providers.MaxRateLimitRetryDelay,
		"Please retry after a short while":    providers.DefaultRateLimitRetryDelay,
		"Please try again in 0s":              providers.DefaultRateLimitRetryDelay,
		"Please try again in -1s":             providers.DefaultRateLimitRetryDelay,
		"Please try again in 0.0000000004s":   time.Nanosecond,
		"Please try again in NaNs":            providers.DefaultRateLimitRetryDelay,
		"Please try again in 1e309s":          providers.DefaultRateLimitRetryDelay,
		"context. Please try again in 0.25s.": 250 * time.Millisecond,
	}
	for message, want := range tests {
		t.Run(message, func(t *testing.T) {
			if got := policy.ParseRateLimitRetryDelay(message); got != want {
				t.Fatalf("delay = %s, want %s", got, want)
			}
		})
	}
}

func TestPolicyNormalizesStatusAndSupportsZeroValue(t *testing.T) {
	var policy Policy
	if got := policy.NormalizeTerminalStatus("  FaIlEd "); got != "failed" {
		t.Fatalf("normalized status = %q, want failed", got)
	}
	if got := policy.ParseRateLimitRetryDelay("Please try again in 1s"); got != time.Second {
		t.Fatalf("zero-value policy delay = %s, want 1s", got)
	}
	var nilPolicy *Policy
	if got := nilPolicy.ParseRateLimitRetryDelay("Please try again in 1s"); got != time.Second {
		t.Fatalf("nil policy delay = %s, want 1s", got)
	}
}
