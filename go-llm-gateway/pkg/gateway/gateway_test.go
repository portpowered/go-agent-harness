package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestInfer_PreservesProviderHTTPStatusClassification(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		err:  NewProviderHTTPStatusError("fake-provider", http.StatusTooManyRequests, `{"error":"slow down"}`, nil),
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	_, err = gw.Infer(context.Background(), InferenceRequest{Model: "model-a"})
	if err == nil {
		t.Fatal("Infer() expected error, got nil")
	}
	if !errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("gateway error should match provider HTTP status classification")
	}
	if !errors.Is(err, ErrRateLimit) {
		t.Fatal("gateway error should match rate limit classification")
	}
	if errors.Is(err, ErrTransport) {
		t.Fatal("provider HTTP status should not match transport classification")
	}

	var statusErr *ProviderHTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatal("gateway error should expose provider HTTP status details")
	}
	if statusErr.Provider != "fake-provider" {
		t.Fatalf("provider = %q, want fake-provider", statusErr.Provider)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", statusErr.StatusCode, http.StatusTooManyRequests)
	}
}

func TestInfer_PreservesTransportClassification(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "fake-provider",
		err:  NewTransportError("fake-provider", "chat completions", io.ErrUnexpectedEOF),
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	_, err = gw.Infer(context.Background(), InferenceRequest{Model: "model-a"})
	if err == nil {
		t.Fatal("Infer() expected error, got nil")
	}
	if !errors.Is(err, ErrTransport) {
		t.Fatal("gateway error should match transport classification")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("gateway error should preserve transport cause")
	}
	if errors.Is(err, ErrProviderHTTPStatus) {
		t.Fatal("transport error should not match provider HTTP status classification")
	}

	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatal("gateway error should expose transport details")
	}
	if transportErr.Operation != "chat completions" {
		t.Fatalf("operation = %q, want chat completions", transportErr.Operation)
	}
}
