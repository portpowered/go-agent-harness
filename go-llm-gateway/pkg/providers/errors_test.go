package providers

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

func TestErrorClassification_DistinguishesRuntimeOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "context cancellation", err: context.Canceled, want: ErrorClassCancellation},
		{name: "taxonomy cancellation", err: ErrCancellation, want: ErrorClassCancellation},
		{name: "replay mismatch", err: ErrReplayMismatch, want: ErrorClassReplayMismatch},
		{name: "partial output", err: ErrPartialOutput, want: ErrorClassPartialOutput},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ErrorClassification(tc.err); got != tc.want {
				t.Fatalf("ErrorClassification(%v) = %q, want %q", tc.err, got, tc.want)
			}
			for _, providerClass := range []error{ErrProviderRejected, ErrTransport, ErrInvalidRequest, ErrUnsupportedRequest} {
				if errors.Is(tc.err, providerClass) {
					t.Fatalf("%v should not match provider class %v", tc.err, providerClass)
				}
			}
		})
	}
}

func TestNewStreamErrorValue_PreservesTypedErrorAndTerminalClassification(t *testing.T) {
	t.Parallel()

	err := NewProviderHTTPError("fake-provider", 429, "slow down")

	value := NewStreamErrorValue(err)
	if value.Message == "" {
		t.Fatal("stream error should retain readable message")
	}
	if value.Classification != ErrorClassRateLimited {
		t.Fatalf("classification = %q, want %q", value.Classification, ErrorClassRateLimited)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceProvider)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
	if !errors.Is(value.Err, ErrProviderRejected) {
		t.Fatal("stream error should preserve provider rejection classification")
	}
	if !errors.Is(value.Err, ErrRateLimited) {
		t.Fatal("stream error should preserve rate limit classification")
	}
	var providerErr *ProviderError
	if !errors.As(value.Err, &providerErr) {
		t.Fatal("stream error should expose provider error details")
	}
	if providerErr.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", providerErr.StatusCode)
	}
}

func TestNewStreamTransportErrorValue_PreservesRuntimeClassification(t *testing.T) {
	t.Parallel()

	value := NewStreamTransportErrorValue(io.ErrUnexpectedEOF)
	if value.Classification != ErrorClassTransport {
		t.Fatalf("classification = %q, want %q", value.Classification, ErrorClassTransport)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceProvider)
	}
	if !errors.Is(value.Err, io.ErrUnexpectedEOF) {
		t.Fatal("stream error should preserve runtime cause")
	}
}
