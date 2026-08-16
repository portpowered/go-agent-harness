package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestS4ProviderHTTPErrorTable(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		detail      string
		wantMessage string
		wantClass   string
		wantCause   error
		wantRetry   bool
	}{
		{name: "400 invalid request", status: 400, detail: "bad payload", wantMessage: "openai: api error 400: bad payload", wantClass: ErrorClassInvalidRequest, wantCause: ErrInvalidRequest, wantRetry: false},
		{name: "422 invalid request", status: 422, detail: "unprocessable", wantMessage: "openai: api error 422: unprocessable", wantClass: ErrorClassInvalidRequest, wantCause: ErrInvalidRequest, wantRetry: false},
		{name: "401 authentication", status: 401, detail: "missing token", wantMessage: "openai: api error 401: missing token", wantClass: ErrorClassAuthentication, wantCause: ErrAuthentication, wantRetry: false},
		{name: "403 authentication", status: 403, detail: "forbidden", wantMessage: "openai: api error 403: forbidden", wantClass: ErrorClassAuthentication, wantCause: ErrAuthentication, wantRetry: false},
		{name: "408 rate limited", status: 408, detail: "request timeout", wantMessage: "openai: api error 408: request timeout", wantClass: ErrorClassRateLimited, wantCause: ErrRateLimited, wantRetry: true},
		{name: "409 rate limited", status: 409, detail: "conflict", wantMessage: "openai: api error 409: conflict", wantClass: ErrorClassRateLimited, wantCause: ErrRateLimited, wantRetry: true},
		{name: "425 rate limited", status: 425, detail: "too early", wantMessage: "openai: api error 425: too early", wantClass: ErrorClassRateLimited, wantCause: ErrRateLimited, wantRetry: true},
		{name: "429 rate limited", status: 429, detail: "slow down", wantMessage: "openai: api error 429: slow down", wantClass: ErrorClassRateLimited, wantCause: ErrRateLimited, wantRetry: true},
		{name: "500 transport", status: 500, detail: "server failed", wantMessage: "openai: api error 500: server failed", wantClass: ErrorClassTransport, wantCause: ErrTransport, wantRetry: true},
		{name: "599 transport", status: 599, detail: "upstream failed", wantMessage: "openai: api error 599: upstream failed", wantClass: ErrorClassTransport, wantCause: ErrTransport, wantRetry: true},
		{name: "other rejected", status: 404, detail: "not found", wantMessage: "openai: api error 404: not found", wantClass: ErrorClassProviderRejected, wantCause: ErrProviderRejected, wantRetry: false},
		{name: "zero status rejected", status: 0, detail: "no status", wantMessage: "openai: no status", wantClass: ErrorClassProviderRejected, wantCause: ErrProviderRejected, wantRetry: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := NewProviderHTTPError("openai", tc.status, tc.detail)
			if got := err.Error(); got != tc.wantMessage {
				t.Fatalf("Error() = %q, want %q", got, tc.wantMessage)
			}
			if err.Provider != "openai" || err.StatusCode != tc.status || err.Detail != tc.detail {
				t.Fatalf("ProviderError fields = %+v, want provider openai status %d detail %q", err, tc.status, tc.detail)
			}
			if !errors.Is(err, ErrProviderRejected) {
				t.Fatal("every HTTP error must retain ErrProviderRejected")
			}
			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("errors.Is(error, %v) = false", tc.wantCause)
			}
			if got := ErrorClassification(err); got != tc.wantClass {
				t.Fatalf("ErrorClassification() = %q, want %q", got, tc.wantClass)
			}
			if got := IsRetryable(err); got != tc.wantRetry {
				t.Fatalf("IsRetryable() = %v, want %v", got, tc.wantRetry)
			}
			var typed *ProviderError
			if !errors.As(err, &typed) || typed != err {
				t.Fatalf("errors.As() = %v, want the constructed *ProviderError", typed)
			}
		})
	}
}

func TestS4ProviderErrorFormattingBranches(t *testing.T) {
	tests := []struct {
		name string
		err  *ProviderError
		want string
	}{
		{name: "status and detail", err: &ProviderError{Provider: "p", StatusCode: 400, Detail: "bad"}, want: "p: api error 400: bad"},
		{name: "status without detail", err: &ProviderError{Provider: "p", StatusCode: 400}, want: "p: api error 400"},
		{name: "fallback provider with status and detail", err: &ProviderError{StatusCode: 400, Detail: "bad"}, want: "provider: api error 400: bad"},
		{name: "fallback provider with status", err: &ProviderError{StatusCode: 400}, want: "provider: api error 400"},
		{name: "detail without status", err: &ProviderError{Provider: "p", Detail: "bad"}, want: "p: bad"},
		{name: "provider error without status or detail", err: &ProviderError{Provider: "p"}, want: "p: provider error"},
		{name: "fallback provider without status or detail", err: &ProviderError{}, want: "provider: provider error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
			if got := IsRetryable(tc.err); got {
				t.Fatalf("IsRetryable() = %v, want false for an unclassified formatting branch", got)
			}
		})
	}
}

func TestS4ValidationErrorConstructorsAndFormatting(t *testing.T) {
	tests := []struct {
		name      string
		err       *ValidationError
		want      string
		wantCause error
		wantClass string
	}{
		{
			name:      "invalid constructor detail",
			err:       NewInvalidRequestError("openai", "messages", "messages are required"),
			want:      "messages are required",
			wantCause: ErrInvalidRequest,
			wantClass: ErrorClassInvalidRequest,
		},
		{
			name:      "invalid constructor generated text",
			err:       NewInvalidRequestError("openai", "temperature", ""),
			want:      "openai: temperature is invalid",
			wantCause: ErrInvalidRequest,
			wantClass: ErrorClassInvalidRequest,
		},
		{
			name:      "unsupported constructor generated text",
			err:       NewUnsupportedRequestError("openai", "model", "gpt-xyz", []string{"gpt-4o", "gpt-4.1"}, ""),
			want:      `openai: model "gpt-xyz" is not supported (supported: gpt-4o, gpt-4.1)`,
			wantCause: ErrUnsupportedRequest,
			wantClass: ErrorClassUnsupportedRequest,
		},
		{
			name:      "unsupported constructor detail",
			err:       NewUnsupportedRequestError("openai", "audio", "pcm", []string{"opus"}, "audio format rejected"),
			want:      "audio format rejected",
			wantCause: ErrUnsupportedRequest,
			wantClass: ErrorClassUnsupportedRequest,
		},
		{
			name:      "feature and requested without provider",
			err:       &ValidationError{Feature: "model", Requested: "x", Supported: []string{"a", "b"}, Err: ErrUnsupportedRequest},
			want:      `model "x" is not supported (supported: a, b)`,
			wantCause: ErrUnsupportedRequest,
			wantClass: ErrorClassUnsupportedRequest,
		},
		{
			name:      "feature without requested",
			err:       &ValidationError{Provider: "p", Feature: "temperature", Supported: []string{"0", "1"}, Err: ErrInvalidRequest},
			want:      "p: temperature is invalid (supported: 0, 1)",
			wantCause: ErrInvalidRequest,
			wantClass: ErrorClassInvalidRequest,
		},
		{
			name:      "provider without feature",
			err:       &ValidationError{Provider: "p", Err: ErrInvalidRequest},
			want:      "p: invalid request",
			wantCause: ErrInvalidRequest,
			wantClass: ErrorClassInvalidRequest,
		},
		{
			name:      "empty validation error",
			err:       &ValidationError{Err: ErrInvalidRequest},
			want:      "invalid request",
			wantCause: ErrInvalidRequest,
			wantClass: ErrorClassInvalidRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
			if !errors.Is(tc.err, tc.wantCause) {
				t.Fatalf("errors.Is(error, %v) = false", tc.wantCause)
			}
			if got := ErrorClassification(tc.err); got != tc.wantClass {
				t.Fatalf("ErrorClassification() = %q, want %q", got, tc.wantClass)
			}
			if got := IsRetryable(tc.err); got {
				t.Fatalf("IsRetryable() = %v, want false for validation class %q", got, tc.wantClass)
			}
			var typed *ValidationError
			if !errors.As(tc.err, &typed) || typed != tc.err {
				t.Fatalf("errors.As() = %v, want the constructed *ValidationError", typed)
			}
		})
	}

	invalid := NewInvalidRequestError("openai", "messages", "bad messages")
	if invalid.Provider != "openai" || invalid.Feature != "messages" || invalid.Detail != "bad messages" || invalid.Requested != "" || len(invalid.Supported) != 0 {
		t.Fatalf("NewInvalidRequestError fields = %+v", invalid)
	}
	unsupported := NewUnsupportedRequestError("openai", "model", "unknown", []string{"gpt-4o"}, "")
	if unsupported.Provider != "openai" || unsupported.Feature != "model" || unsupported.Requested != "unknown" || strings.Join(unsupported.Supported, ",") != "gpt-4o" {
		t.Fatalf("NewUnsupportedRequestError fields = %+v", unsupported)
	}
}

func TestS4ErrorClassificationTableAndPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		want      string
		wantRetry bool
	}{
		{name: "nil", err: nil, want: "", wantRetry: false},
		{name: "context canceled", err: context.Canceled, want: ErrorClassCancellation, wantRetry: false},
		{name: "context deadline is not cancellation", err: context.DeadlineExceeded, want: ErrorClassUnknown, wantRetry: true},
		{name: "taxonomy cancellation", err: ErrCancellation, want: ErrorClassCancellation, wantRetry: false},
		{name: "replay incomplete", err: ErrReplayIncomplete, want: ErrorClassReplayIncomplete, wantRetry: false},
		{name: "replay mismatch", err: ErrReplayMismatch, want: ErrorClassReplayMismatch, wantRetry: false},
		{name: "partial output", err: ErrPartialOutput, want: ErrorClassPartialOutput, wantRetry: false},
		{name: "authentication", err: ErrAuthentication, want: ErrorClassAuthentication, wantRetry: false},
		{name: "rate limited", err: ErrRateLimited, want: ErrorClassRateLimited, wantRetry: true},
		{name: "invalid request", err: ErrInvalidRequest, want: ErrorClassInvalidRequest, wantRetry: false},
		{name: "unsupported request", err: ErrUnsupportedRequest, want: ErrorClassUnsupportedRequest, wantRetry: false},
		{name: "transport", err: ErrTransport, want: ErrorClassTransport, wantRetry: true},
		{name: "provider rejected", err: ErrProviderRejected, want: ErrorClassProviderRejected, wantRetry: false},
		{name: "unknown", err: errors.New("unclassified"), want: ErrorClassUnknown, wantRetry: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorClassification(tc.err); got != tc.want {
				t.Fatalf("ErrorClassification(%v) = %q, want %q", tc.err, got, tc.want)
			}
			if got := IsRetryable(tc.err); got != tc.wantRetry {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.wantRetry)
			}
		})
	}

	ordered := []struct {
		sentinel  error
		class     string
		retryable bool
	}{
		{ErrCancellation, ErrorClassCancellation, false},
		{ErrReplayIncomplete, ErrorClassReplayIncomplete, false},
		{ErrReplayMismatch, ErrorClassReplayMismatch, false},
		{ErrPartialOutput, ErrorClassPartialOutput, false},
		{ErrAuthentication, ErrorClassAuthentication, false},
		{ErrRateLimited, ErrorClassRateLimited, true},
		{ErrInvalidRequest, ErrorClassInvalidRequest, false},
		{ErrUnsupportedRequest, ErrorClassUnsupportedRequest, false},
		{ErrTransport, ErrorClassTransport, true},
		{ErrProviderRejected, ErrorClassProviderRejected, false},
	}
	for high, higher := range ordered {
		for low := high + 1; low < len(ordered); low++ {
			t.Run(fmt.Sprintf("precedence/%s_over_%s", higher.class, ordered[low].class), func(t *testing.T) {
				err := errors.Join(ordered[low].sentinel, higher.sentinel)
				if got := ErrorClassification(err); got != higher.class {
					t.Fatalf("ErrorClassification(%v) = %q, want %q", err, got, higher.class)
				}
				if got := IsRetryable(err); got != higher.retryable {
					t.Fatalf("IsRetryable(%v) = %v, want %v", err, got, higher.retryable)
				}
			})
		}
	}
}

func TestS4TwoLevelWrappingPreservesTypedErrorsAndJoinedSentinels(t *testing.T) {
	providerErr := NewProviderHTTPError("fake", 429, "slow down")
	wrappedProvider := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", providerErr))
	if !errors.Is(wrappedProvider, ErrProviderRejected) || !errors.Is(wrappedProvider, ErrRateLimited) {
		t.Fatal("two wrapping levels must preserve provider taxonomy sentinels")
	}
	if got := ErrorClassification(wrappedProvider); got != ErrorClassRateLimited {
		t.Fatalf("wrapped provider classification = %q, want %q", got, ErrorClassRateLimited)
	}
	if got := IsRetryable(wrappedProvider); !got {
		t.Fatalf("wrapped provider retryability = %v, want true", got)
	}
	var gotProvider *ProviderError
	if !errors.As(wrappedProvider, &gotProvider) || gotProvider.Provider != "fake" || gotProvider.StatusCode != 429 || gotProvider.Detail != "slow down" {
		t.Fatalf("wrapped provider details = %+v", gotProvider)
	}

	validationErr := NewUnsupportedRequestError("fake", "model", "unknown", []string{"known"}, "")
	wrappedValidation := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", validationErr))
	if !errors.Is(wrappedValidation, ErrUnsupportedRequest) {
		t.Fatal("two wrapping levels must preserve validation taxonomy")
	}
	if got := ErrorClassification(wrappedValidation); got != ErrorClassUnsupportedRequest {
		t.Fatalf("wrapped validation classification = %q, want %q", got, ErrorClassUnsupportedRequest)
	}
	if got := IsRetryable(wrappedValidation); got {
		t.Fatalf("wrapped validation retryability = %v, want false", got)
	}
	var gotValidation *ValidationError
	if !errors.As(wrappedValidation, &gotValidation) || gotValidation.Provider != "fake" || gotValidation.Requested != "unknown" || strings.Join(gotValidation.Supported, ",") != "known" {
		t.Fatalf("wrapped validation details = %+v", gotValidation)
	}

	joinedSentinels := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", errors.Join(ErrProviderRejected, ErrRateLimited)))
	if !errors.Is(joinedSentinels, ErrProviderRejected) || !errors.Is(joinedSentinels, ErrRateLimited) {
		t.Fatal("two wrapping levels must preserve every joined sentinel")
	}
	if got := ErrorClassification(joinedSentinels); got != ErrorClassRateLimited {
		t.Fatalf("joined sentinel classification = %q, want %q", got, ErrorClassRateLimited)
	}
	if got := IsRetryable(joinedSentinels); !got {
		t.Fatalf("joined sentinel retryability = %v, want true", got)
	}

	joinedTyped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", errors.Join(providerErr, ErrPartialOutput)))
	if got := ErrorClassification(joinedTyped); got != ErrorClassPartialOutput {
		t.Fatalf("joined typed classification = %q, want %q", got, ErrorClassPartialOutput)
	}
	if got := IsRetryable(joinedTyped); got {
		t.Fatalf("joined typed retryability = %v, want false after partial-output precedence", got)
	}
	if !errors.Is(joinedTyped, ErrPartialOutput) {
		t.Fatal("joined typed error lost partial-output sentinel")
	}
	if !errors.As(joinedTyped, &gotProvider) || gotProvider.StatusCode != 429 {
		t.Fatalf("joined typed provider details = %+v", gotProvider)
	}
}

func TestS4StreamValueTablePreservesTerminalContractAndCauses(t *testing.T) {
	providerErr := NewProviderHTTPError("fake", 429, "slow down")
	wrappedTransport := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", io.ErrUnexpectedEOF))
	unknownErr := errors.New("reader exploded")

	tests := []struct {
		name      string
		build     func(error) *messages.ErrorValue
		err       error
		wantClass string
		wantRetry bool
	}{
		{name: "stream nil", build: NewStreamErrorValue, wantClass: "", wantRetry: false},
		{name: "stream provider error", build: NewStreamErrorValue, err: providerErr, wantClass: ErrorClassRateLimited, wantRetry: true},
		{name: "stream cancellation", build: NewStreamErrorValue, err: context.Canceled, wantClass: ErrorClassCancellation, wantRetry: false},
		{name: "stream unknown", build: NewStreamErrorValue, err: unknownErr, wantClass: ErrorClassUnknown, wantRetry: false},
		{name: "transport nil", build: NewStreamTransportErrorValue, wantClass: "", wantRetry: false},
		{name: "transport unknown", build: NewStreamTransportErrorValue, err: unknownErr, wantClass: ErrorClassTransport, wantRetry: false},
		{name: "transport wrapped cause", build: NewStreamTransportErrorValue, err: wrappedTransport, wantClass: ErrorClassTransport, wantRetry: false},
		{name: "transport preserves known class", build: NewStreamTransportErrorValue, err: providerErr, wantClass: ErrorClassRateLimited, wantRetry: true},
		{name: "transport preserves invalid class", build: NewStreamTransportErrorValue, err: ErrInvalidRequest, wantClass: ErrorClassInvalidRequest, wantRetry: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := tc.build(tc.err)
			if value == nil || value.Type != "error" {
				t.Fatalf("stream value = %#v, want error value", value)
			}
			if got := value.Classification; got != tc.wantClass {
				t.Fatalf("classification = %q, want %q", got, tc.wantClass)
			}
			if got := IsRetryable(tc.err); got != tc.wantRetry {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.wantRetry)
			}
			if tc.err == nil {
				if value.Message != "" || value.Err != nil {
					t.Fatalf("nil error stream value = message %q cause %v", value.Message, value.Err)
				}
			} else {
				if value.Message == "" || value.Message != tc.err.Error() {
					t.Fatalf("message = %q, want readable %q", value.Message, tc.err.Error())
				}
				if value.Err != tc.err {
					t.Fatalf("preserved cause = %v, want original %v", value.Err, tc.err)
				}
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
		})
	}

	value := NewStreamErrorValue(providerErr)
	if !errors.Is(value.Err, ErrProviderRejected) || !errors.Is(value.Err, ErrRateLimited) {
		t.Fatal("stream value lost provider taxonomy")
	}
	var typed *ProviderError
	if !errors.As(value.Err, &typed) || typed.StatusCode != 429 {
		t.Fatalf("stream typed cause = %+v, want status 429 provider error", typed)
	}
	if !IsRetryable(value.Err) {
		t.Fatal("stream value lost provider retryability")
	}
	transportValue := NewStreamTransportErrorValue(wrappedTransport)
	if !errors.Is(transportValue.Err, io.ErrUnexpectedEOF) {
		t.Fatal("transport stream value lost wrapped reader cause")
	}
	wrappedStreamProvider := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", providerErr))
	wrappedStreamValue := NewStreamErrorValue(wrappedStreamProvider)
	if !errors.Is(wrappedStreamValue.Err, ErrProviderRejected) || !errors.Is(wrappedStreamValue.Err, ErrRateLimited) {
		t.Fatal("stream value lost two-level wrapped provider taxonomy")
	}
	if !IsRetryable(wrappedStreamValue.Err) {
		t.Fatal("stream value lost two-level wrapped provider retryability")
	}
	var wrappedStreamTyped *ProviderError
	if !errors.As(wrappedStreamValue.Err, &wrappedStreamTyped) || wrappedStreamTyped.StatusCode != 429 {
		t.Fatalf("two-level wrapped stream cause = %+v, want status 429 provider error", wrappedStreamTyped)
	}
}

func TestS4RetryabilityPolicyMatrix(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		class     string
		retryable bool
	}{
		{name: "rate limited", err: ErrRateLimited, class: ErrorClassRateLimited, retryable: true},
		{name: "transport", err: ErrTransport, class: ErrorClassTransport, retryable: true},
		{name: "wrapped transport", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrTransport)), class: ErrorClassTransport, retryable: true},
		{name: "authentication", err: ErrAuthentication, class: ErrorClassAuthentication, retryable: false},
		{name: "invalid request", err: ErrInvalidRequest, class: ErrorClassInvalidRequest, retryable: false},
		{name: "unsupported request", err: ErrUnsupportedRequest, class: ErrorClassUnsupportedRequest, retryable: false},
		{name: "provider rejected", err: ErrProviderRejected, class: ErrorClassProviderRejected, retryable: false},
		{name: "wrapped deadline", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", context.DeadlineExceeded)), class: ErrorClassUnknown, retryable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorClassification(tc.err); got != tc.class {
				t.Fatalf("ErrorClassification(%v) = %q, want %q", tc.err, got, tc.class)
			}
			if got := IsRetryable(tc.err); got != tc.retryable {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.retryable)
			}
		})
	}
}

// Keep the original focused runtime checks alongside the exhaustive S4 tables
// so future changes retain a small, easy-to-read regression signal.
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
		{name: "replay incomplete", err: ErrReplayIncomplete, want: ErrorClassReplayIncomplete},
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
