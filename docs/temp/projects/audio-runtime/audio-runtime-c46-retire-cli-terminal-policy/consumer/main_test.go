package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
)

func TestPublicTerminalPolicyBehaviorMatrix(t *testing.T) {
	policy := providerswire.NewTerminalPolicy()

	tests := []struct {
		name         string
		terminal     *messages.MessageEndValue
		wantDelay    time.Duration
		wantEligible bool
	}{
		{
			name: "nil terminal",
		},
		{
			name: "failed explicit provider fields",
			terminal: &messages.MessageEndValue{
				Status:               " FAILED ",
				ProviderErrorCode:    providers.RateLimitRetryCode,
				ProviderErrorMessage: "Please try again in 1.668s.",
			},
			wantDelay:    1668 * time.Millisecond,
			wantEligible: true,
		},
		{
			name: "legacy status details",
			terminal: &messages.MessageEndValue{
				Status:        "failed",
				StatusDetails: "reason=error, code=rate_limit_exceeded, message=Please try again in 1.5s, preserve this comma.",
			},
			wantDelay:    1500 * time.Millisecond,
			wantEligible: true,
		},
		{
			name: "cancellation is not retryable",
			terminal: &messages.MessageEndValue{
				Status:               "failed",
				ProviderErrorCode:    providers.RateLimitRetryCode,
				ProviderErrorMessage: "Please try again in 1s",
				TerminalReason:       messages.TerminalReasonCancellation,
			},
		},
		{
			name: "non-failed status is not retryable",
			terminal: &messages.MessageEndValue{
				Status:               "incomplete",
				ProviderErrorCode:    providers.RateLimitRetryCode,
				ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "provider code is case sensitive",
			terminal: &messages.MessageEndValue{
				Status:               "failed",
				ProviderErrorCode:    "RATE_LIMIT_EXCEEDED",
				ProviderErrorMessage: "Please try again in 1s",
			},
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

func TestPublicTerminalPolicyMetadataAndDelayBounds(t *testing.T) {
	policy := providerswire.NewTerminalPolicy()
	legacyMessage := "Please try again in 1.5s, preserve this comma-bearing provider context."
	legacy := &messages.MessageEndValue{
		StatusDetails: "reason=error, code=rate_limit_exceeded, message=" + legacyMessage,
	}
	if got := policy.ProviderTerminalErrorCode(legacy); got != providers.RateLimitRetryCode {
		t.Fatalf("legacy code = %q, want %q", got, providers.RateLimitRetryCode)
	}
	if got := policy.ProviderTerminalErrorMessage(legacy); got != legacyMessage {
		t.Fatalf("legacy message = %q, want %q", got, legacyMessage)
	}

	explicit := &messages.MessageEndValue{
		ProviderErrorCode:    " " + providers.RateLimitRetryCode + " ",
		ProviderErrorMessage: " Please try again in 0.25s ",
		StatusDetails:        "code=wrong, message=Please try again in 45s",
	}
	if got := policy.ProviderTerminalErrorCode(explicit); got != providers.RateLimitRetryCode {
		t.Fatalf("explicit code = %q, want %q", got, providers.RateLimitRetryCode)
	}
	if got := policy.ProviderTerminalErrorMessage(explicit); got != "Please try again in 0.25s" {
		t.Fatalf("explicit message = %q, want explicit message", got)
	}

	long := strings.Repeat("x", providers.MaxLegacyStatusDetailBytes+17)
	if got := policy.LegacyStatusDetailField("message="+long, "message"); len(got) != providers.MaxLegacyStatusDetailBytes {
		t.Fatalf("bounded legacy field length = %d, want %d", len(got), providers.MaxLegacyStatusDetailBytes)
	}
	if got := policy.LegacyStatusDetailField("malformed,other=value", "missing"); got != "" {
		t.Fatalf("missing legacy field = %q, want empty", got)
	}

	delays := map[string]time.Duration{
		"Please try again in .5s":                 500 * time.Millisecond,
		"Please try again in 45s":                 providers.MaxRateLimitRetryDelay,
		"Please try again in 0s":                  providers.DefaultRateLimitRetryDelay,
		"Please try again in 0.0000000004s":       time.Nanosecond,
		"provider did not include retry guidance": providers.DefaultRateLimitRetryDelay,
	}
	for message, want := range delays {
		if got := policy.ParseRateLimitRetryDelay(message); got != want {
			t.Errorf("delay for %q = %s, want %s", message, got, want)
		}
	}
	if got := policy.NormalizeTerminalStatus("  FaIlEd "); got != "failed" {
		t.Fatalf("normalized status = %q, want failed", got)
	}
}

func TestWrongOracleMutationControls(t *testing.T) {
	policy := providerswire.NewTerminalPolicy()
	terminal := &messages.MessageEndValue{
		Status:               "failed",
		ProviderErrorCode:    providers.RateLimitRetryCode,
		ProviderErrorMessage: "Please try again in 1.668s.",
	}
	gotDelay, gotEligible := policy.RateLimitRetryDecision(terminal)

	wantDelay := 1668 * time.Millisecond
	wantEligible := true
	switch os.Getenv("C46_WRONG_ORACLE") {
	case "":
		// The unmutated oracle is the expected passing path.
	case "delay":
		wantDelay = 1667 * time.Millisecond
	case "eligibility":
		wantEligible = false
	default:
		t.Fatalf("unknown C46_WRONG_ORACLE value %q", os.Getenv("C46_WRONG_ORACLE"))
	}
	if gotDelay != wantDelay {
		t.Fatalf("delay = %s, want %s", gotDelay, wantDelay)
	}
	if gotEligible != wantEligible {
		t.Fatalf("eligible = %t, want %t", gotEligible, wantEligible)
	}
}
