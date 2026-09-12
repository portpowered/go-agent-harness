package service

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy"
)

func TestDecideClassificationMatrix(t *testing.T) {
	tests := []struct {
		name     string
		terminal retrypolicy.Terminal
		want     retrypolicy.Decision
	}{
		{name: "zero terminal", want: retrypolicy.Decision{}},
		{
			name: "failed status is normalized",
			terminal: retrypolicy.Terminal{
				Status: " FAILED ", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1.668s.",
			},
			want: retrypolicy.Decision{Delay: 1668 * time.Millisecond, Eligible: true},
		},
		{
			name: "incomplete status is excluded",
			terminal: retrypolicy.Terminal{
				Status: "incomplete", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "cancelled status is excluded",
			terminal: retrypolicy.Terminal{
				Status: "cancelled", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "completed status is excluded",
			terminal: retrypolicy.Terminal{
				Status: "completed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "cancellation reason is excluded",
			terminal: retrypolicy.Terminal{
				Status: "failed", TerminalReason: "cancellation", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "substring code is excluded",
			terminal: retrypolicy.Terminal{
				Status: "failed", ProviderErrorCode: "quota_rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "case changed code is excluded",
			terminal: retrypolicy.Terminal{
				Status: "failed", ProviderErrorCode: "RATE_LIMIT_EXCEEDED", ProviderErrorMessage: "Please try again in 1s",
			},
		},
		{
			name: "legacy compact details",
			terminal: retrypolicy.Terminal{
				Status: "failed", StatusDetails: "reason=error, code=rate_limit_exceeded, message=Please try again in 1.5s.",
			},
			want: retrypolicy.Decision{Delay: 1500 * time.Millisecond, Eligible: true},
		},
		{
			name: "legacy parser stops at compatibility bound",
			terminal: retrypolicy.Terminal{
				Status: "failed", StatusDetails: strings.Repeat("x", 256) + ", code=rate_limit_exceeded, message=Please try again in 1s",
			},
		},
	}

	policy := New()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := policy.Decide(test.terminal); got != test.want {
				t.Fatalf("decision = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestDecideDelayBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    time.Duration
	}{
		{name: "contextual decimal", message: "Request rate limit reached. Please try again in 0.25s.", want: 250 * time.Millisecond},
		{name: "leading decimal", message: "Please try again in .5s", want: 500 * time.Millisecond},
		{name: "default missing", message: "", want: 2 * time.Second},
		{name: "default malformed", message: "Please retry after a short while", want: 2 * time.Second},
		{name: "default zero", message: "Please try again in 0s", want: 2 * time.Second},
		{name: "default negative", message: "Please try again in -1s", want: 2 * time.Second},
		{name: "default NaN", message: "Please try again in NaNs", want: 2 * time.Second},
		{name: "default infinity", message: "Please try again in " + strings.Repeat("9", 400) + "s", want: 2 * time.Second},
		{name: "positive sub-nanosecond", message: "Please try again in 0.0000000004s", want: time.Nanosecond},
		{name: "nearest nanosecond", message: "Please try again in 0.0000000016s", want: 2 * time.Nanosecond},
		{name: "maximum cap", message: "Please try again in 45s", want: 15 * time.Second},
	}

	policy := New()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := policy.Decide(retrypolicy.Terminal{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: test.message})
			want := retrypolicy.Decision{Delay: test.want, Eligible: true}
			if got != want {
				t.Fatalf("decision = %#v, want %#v", got, want)
			}
		})
	}
}

func TestExplicitProviderFieldsOverrideLegacyDetails(t *testing.T) {
	policy := New()
	terminal := retrypolicy.Terminal{
		Status:               "failed",
		StatusDetails:        "code=not_rate_limited, message=Please try again in 45s",
		ProviderErrorCode:    " rate_limit_exceeded ",
		ProviderErrorMessage: " Please try again in 0.25s ",
	}
	if got := policy.ProviderErrorCode(terminal); got != "rate_limit_exceeded" {
		t.Fatalf("code = %q, want explicit trimmed code", got)
	}
	if got := policy.ProviderErrorMessage(terminal); got != "Please try again in 0.25s" {
		t.Fatalf("message = %q, want explicit trimmed message", got)
	}
	if got := policy.Decide(terminal); got != (retrypolicy.Decision{Delay: 250 * time.Millisecond, Eligible: true}) {
		t.Fatalf("decision = %#v, want explicit-field decision", got)
	}
}

func TestLegacyMessagePreservesCommasAndBoundsValue(t *testing.T) {
	policy := New()
	terminal := retrypolicy.Terminal{StatusDetails: "reason=error, code=rate_limit_exceeded, message=Please try again in 1.5s, provider context"}
	if got := policy.ProviderErrorMessage(terminal); got != "Please try again in 1.5s, provider context" {
		t.Fatalf("legacy message = %q, want comma-bearing remainder", got)
	}
	longMessage := "message=" + strings.Repeat("m", 300)
	if got := policy.ProviderErrorMessage(retrypolicy.Terminal{StatusDetails: longMessage}); len(got) != 248 {
		t.Fatalf("legacy message length = %d, want the 256-byte detail bound after the key", len(got))
	}
}

func TestNormalizeStatus(t *testing.T) {
	if got := New().NormalizeStatus("  FaIlEd "); got != "failed" {
		t.Fatalf("normalized status = %q, want failed", got)
	}
}

func TestIndependentServicesDoNotShareState(t *testing.T) {
	first, second := New(), New()
	terminal := retrypolicy.Terminal{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s"}
	want := retrypolicy.Decision{Delay: time.Second, Eligible: true}
	if first.Decide(terminal) != want || second.Decide(terminal) != want {
		t.Fatalf("isolated decisions changed: first=%#v second=%#v", first.Decide(terminal), second.Decide(terminal))
	}
}
