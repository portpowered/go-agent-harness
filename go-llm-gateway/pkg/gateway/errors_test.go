package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestGatewayError_MatchesWrappedClassAndCause(t *testing.T) {
	cause := io.ErrUnexpectedEOF
	err := fmt.Errorf("outer context: %w", NewGatewayError(ErrInvalidRequest, "openai", "bad prompt", cause))

	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatal("wrapped gateway error should match invalid request class")
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped gateway error should preserve underlying cause")
	}

	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatal("wrapped error should expose GatewayError details")
	}
	if gatewayErr.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", gatewayErr.Provider)
	}
}

func TestProviderHTTPStatusError_MatchesStatusClassAndDetails(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantClass  error
	}{
		{name: "bad request", statusCode: http.StatusBadRequest, wantClass: ErrInvalidRequest},
		{name: "unprocessable", statusCode: http.StatusUnprocessableEntity, wantClass: ErrInvalidRequest},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantClass: ErrAuthentication},
		{name: "forbidden", statusCode: http.StatusForbidden, wantClass: ErrAuthorization},
		{name: "not found", statusCode: http.StatusNotFound, wantClass: ErrUnsupportedModel},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, wantClass: ErrRateLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fmt.Errorf("request failed: %w", NewProviderHTTPStatusError("openai", tt.statusCode, `{"error":"failed"}`, nil))

			if !errors.Is(err, ErrProviderHTTPStatus) {
				t.Fatal("provider status error should match provider HTTP status class")
			}
			if !errors.Is(err, tt.wantClass) {
				t.Fatalf("provider status error should match %v", tt.wantClass)
			}

			var statusErr *ProviderHTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatal("provider status error should expose typed status details")
			}
			if statusErr.Provider != "openai" {
				t.Fatalf("provider = %q, want openai", statusErr.Provider)
			}
			if statusErr.StatusCode != tt.statusCode {
				t.Fatalf("status = %d, want %d", statusErr.StatusCode, tt.statusCode)
			}
		})
	}
}

func TestProviderHTTPStatusError_UnknownStatusOnlyMatchesHTTPStatus(t *testing.T) {
	err := NewProviderHTTPStatusError("openai", http.StatusInternalServerError, "server failed", nil)

	if !errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("provider status error should match provider HTTP status class")
	}
	if errors.Is(err, ErrTransport) {
		t.Fatal("provider status error should not match transport")
	}
	if errors.Is(err, ErrRateLimit) {
		t.Fatal("unmapped provider status should not match specific caller-action class")
	}
}

func TestTransportError_MatchesTransportAndPreservesCause(t *testing.T) {
	cause := io.ErrClosedPipe
	err := fmt.Errorf("outer context: %w", NewTransportError("gemini", "generate content", cause))

	if !errors.Is(err, ErrTransport) {
		t.Fatal("transport error should match transport class")
	}
	if !errors.Is(err, cause) {
		t.Fatal("transport error should preserve underlying cause")
	}
	if errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("transport error should not match provider HTTP status")
	}

	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatal("transport error should expose typed transport details")
	}
	if transportErr.Operation != "generate content" {
		t.Fatalf("operation = %q, want generate content", transportErr.Operation)
	}
}

func TestReplayMismatchError_MatchesReplayMismatchOnly(t *testing.T) {
	err := fmt.Errorf("fixture failed: %w", NewReplayMismatchError("recorded request", "actual request", nil))

	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatal("replay mismatch error should match replay mismatch class")
	}
	if errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("replay mismatch should not match provider HTTP status")
	}
	if errors.Is(err, ErrTransport) {
		t.Fatal("replay mismatch should not match transport")
	}

	var replayErr *ReplayMismatchError
	if !errors.As(err, &replayErr) {
		t.Fatal("replay mismatch should expose typed details")
	}
}

func TestReplayPayloadDivergenceError_RendersStructuredDetails(t *testing.T) {
	detail := NewReplayPayloadDivergenceError(
		"JSON pointer /item/content/0/text",
		`"expected text"`,
		`"actual text"`,
	)
	if got, want := detail.Error(), `JSON pointer /item/content/0/text: expected "expected text", actual "actual text"`; got != want {
		t.Fatalf("divergence error = %q, want %q", got, want)
	}

	err := NewReplayMismatchError("recorded event", "actual event", detail)
	if !errors.Is(err, ErrReplayMismatch) {
		t.Fatal("wrapped divergence should retain replay mismatch classification")
	}
	var divergence *ReplayPayloadDivergenceError
	if !errors.As(err, &divergence) {
		t.Fatal("wrapped divergence should expose structured details")
	}
	if divergence.Location != "JSON pointer /item/content/0/text" {
		t.Fatalf("divergence location = %q, want JSON pointer /item/content/0/text", divergence.Location)
	}
}

func TestReplayIncompleteError_MatchesReplayIncompleteOnly(t *testing.T) {
	err := fmt.Errorf("fixture failed: %w", NewReplayIncompleteError("remaining event", "session close", nil))

	if !errors.Is(err, ErrReplayIncomplete) {
		t.Fatal("replay incomplete error should match replay incomplete class")
	}
	if errors.Is(err, ErrReplayMismatch) {
		t.Fatal("replay incomplete should not match replay mismatch")
	}
	if errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("replay incomplete should not match provider HTTP status")
	}
	if errors.Is(err, ErrTransport) {
		t.Fatal("replay incomplete should not match transport")
	}

	var replayErr *ReplayIncompleteError
	if !errors.As(err, &replayErr) {
		t.Fatal("replay incomplete should expose typed details")
	}
}

func TestCancellationError_MatchesCancellationAndContextCause(t *testing.T) {
	err := fmt.Errorf("outer context: %w", NewCancellationError("caller cancelled request", context.Canceled))

	if !errors.Is(err, ErrCancellation) {
		t.Fatal("cancellation error should match gateway cancellation class")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation error should preserve context cancellation cause")
	}
	if errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("cancellation should not match provider HTTP status")
	}
	if errors.Is(err, ErrReplayMismatch) {
		t.Fatal("cancellation should not match replay mismatch")
	}
}
