package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
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

func TestInferStream_PreservesErrorEventClassification(t *testing.T) {
	t.Parallel()

	streamErr := NewProviderHTTPStatusError("fake-provider", http.StatusUnauthorized, `{"error":"bad token"}`, nil)
	provider := &fakeInteractionProvider{
		name: "fake-provider",
		streamMessages: []messages.StreamMessage{
			{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(streamErr),
			},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ch, err := gw.InferStream(context.Background(), InferenceRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("InferStream() error = %v", err)
	}

	var gotErr *messages.ErrorValue
	for msg := range ch {
		if msg.Type != messages.StreamTypeError {
			continue
		}
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("error event value = %T, want *messages.ErrorValue", msg.Value)
		}
		gotErr = value
	}
	if gotErr == nil {
		t.Fatal("expected stream error event")
	}
	if gotErr.Message == "" {
		t.Fatal("error event should retain readable message text")
	}
	if gotErr.Classification != "authentication" {
		t.Fatalf("classification = %q, want authentication", gotErr.Classification)
	}
	if gotErr.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", gotErr.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if gotErr.TerminalProvenance != messages.TerminalProvenanceGateway {
		t.Fatalf("terminal provenance = %q, want %q", gotErr.TerminalProvenance, messages.TerminalProvenanceGateway)
	}
	if gotErr.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", gotErr.OutputState, messages.TerminalOutputNone)
	}
	if !errors.Is(gotErr.Err, ErrProviderHTTPStatus) {
		t.Fatal("stream error should match provider HTTP status classification")
	}
	if !errors.Is(gotErr.Err, ErrAuthentication) {
		t.Fatal("stream error should match authentication classification")
	}

	var statusErr *ProviderHTTPStatusError
	if !errors.As(gotErr.Err, &statusErr) {
		t.Fatal("stream error should expose provider HTTP status details")
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", statusErr.StatusCode, http.StatusUnauthorized)
	}
}

func TestInferStream_ErrorEventClassificationIsPublicAndSerializable(t *testing.T) {
	t.Parallel()

	streamErr := NewProviderHTTPStatusError("fake-provider", http.StatusUnauthorized, `{"error":"bad token"}`, nil)
	provider := &fakeInteractionProvider{
		name: "fake-provider",
		streamMessages: []messages.StreamMessage{
			{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(streamErr),
			},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ch, err := gw.InferStream(context.Background(), InferenceRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("InferStream() error = %v", err)
	}

	var gotErr *messages.ErrorValue
	for msg := range ch {
		if msg.Type != messages.StreamTypeError {
			continue
		}
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("error event value = %T, want *messages.ErrorValue", msg.Value)
		}
		gotErr = value
	}
	if gotErr == nil {
		t.Fatal("expected stream error event")
	}
	if gotErr.Message == "" {
		t.Fatal("error event should retain readable message text")
	}
	if !errors.Is(gotErr.Err, ErrProviderHTTPStatus) {
		t.Fatal("stream error should match provider HTTP status classification")
	}
	if !errors.Is(gotErr.Err, ErrAuthentication) {
		t.Fatal("stream error should match authentication classification")
	}
	var statusErr *ProviderHTTPStatusError
	if !errors.As(gotErr.Err, &statusErr) {
		t.Fatal("stream error should expose provider HTTP status details")
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", statusErr.StatusCode, http.StatusUnauthorized)
	}

	data, err := json.Marshal(gotErr)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var payload struct {
		Type               string `json:"type"`
		Message            string `json:"message"`
		Classification     string `json:"classification"`
		TerminalReason     string `json:"terminal_reason"`
		TerminalProvenance string `json:"terminal_provenance"`
		OutputState        string `json:"output_state"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Type != "error" {
		t.Fatalf("type = %q, want error", payload.Type)
	}
	if payload.Message == "" {
		t.Fatal("serialized error should retain readable message text")
	}
	if payload.Classification != "authentication" {
		t.Fatalf("classification = %q, want authentication", payload.Classification)
	}
	if payload.TerminalReason != string(messages.TerminalReasonTerminalFailure) {
		t.Fatalf("terminal reason = %q, want %q", payload.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if payload.TerminalProvenance != string(messages.TerminalProvenanceGateway) {
		t.Fatalf("terminal provenance = %q, want %q", payload.TerminalProvenance, messages.TerminalProvenanceGateway)
	}
	if payload.OutputState != string(messages.TerminalOutputNone) {
		t.Fatalf("output state = %q, want %q", payload.OutputState, messages.TerminalOutputNone)
	}
}

func TestInferStream_PreservesRuntimeErrorEventClassification(t *testing.T) {
	t.Parallel()

	streamErr := NewTransportError("fake-provider", "direct stream", io.ErrUnexpectedEOF)
	provider := &fakeInteractionProvider{
		name: "fake-provider",
		streamMessages: []messages.StreamMessage{
			{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(streamErr),
			},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	ch, err := gw.InferStream(context.Background(), InferenceRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("InferStream() error = %v", err)
	}

	var gotErr *messages.ErrorValue
	for msg := range ch {
		if msg.Type != messages.StreamTypeError {
			continue
		}
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("error event value = %T, want *messages.ErrorValue", msg.Value)
		}
		gotErr = value
	}
	if gotErr == nil {
		t.Fatal("expected stream error event")
	}
	if gotErr.Classification != "transport" {
		t.Fatalf("classification = %q, want transport", gotErr.Classification)
	}
	if gotErr.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", gotErr.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if gotErr.TerminalProvenance != messages.TerminalProvenanceGateway {
		t.Fatalf("terminal provenance = %q, want %q", gotErr.TerminalProvenance, messages.TerminalProvenanceGateway)
	}
	if gotErr.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", gotErr.OutputState, messages.TerminalOutputNone)
	}
	if !errors.Is(gotErr.Err, ErrTransport) {
		t.Fatal("stream error should match transport classification")
	}
	if !errors.Is(gotErr.Err, io.ErrUnexpectedEOF) {
		t.Fatal("stream error should preserve runtime cause")
	}
	if errors.Is(gotErr.Err, ErrProviderHTTPStatus) {
		t.Fatal("runtime stream error should not match provider HTTP status classification")
	}
}

func TestInferStream_TerminalErrorNormalizationPreservesProviderCapabilities(t *testing.T) {
	t.Parallel()

	provider := &fakeInteractionProvider{
		name: "capability-provider",
		caps: ProviderCapabilities{
			Provider: "capability-provider",
			Stateless: capabilities.StatelessCapabilities{
				Streaming: capabilities.Supported("stream API supported"),
				Tools:     capabilities.Unsupported("tools disabled for this provider"),
			},
		},
		streamMessages: []messages.StreamMessage{
			{
				Type:  messages.StreamTypeError,
				Value: messages.NewErrorValueWithError(NewTransportError("capability-provider", "stream", io.ErrUnexpectedEOF)),
			},
		},
	}
	gw, err := NewGateway(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	before := gw.Capabilities()
	ch, err := gw.InferStream(context.Background(), InferenceRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("InferStream() error = %v", err)
	}

	var gotErr *messages.ErrorValue
	for msg := range ch {
		if msg.Type != messages.StreamTypeError {
			continue
		}
		value, ok := msg.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("error event value = %T, want *messages.ErrorValue", msg.Value)
		}
		gotErr = value
	}
	after := gw.Capabilities()

	if gotErr == nil {
		t.Fatal("expected stream error event")
	}
	if gotErr.Classification != "transport" {
		t.Fatalf("classification = %q, want transport", gotErr.Classification)
	}
	if gotErr.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", gotErr.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if gotErr.TerminalProvenance != messages.TerminalProvenanceGateway {
		t.Fatalf("terminal provenance = %q, want %q", gotErr.TerminalProvenance, messages.TerminalProvenanceGateway)
	}
	if gotErr.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", gotErr.OutputState, messages.TerminalOutputNone)
	}
	if before.Stateless.Streaming.State != CapabilityStateSupported || after.Stateless.Streaming.State != CapabilityStateSupported {
		t.Fatalf("streaming capability changed: before=%q after=%q", before.Stateless.Streaming.State, after.Stateless.Streaming.State)
	}
	if before.Stateless.Tools.State != CapabilityStateUnsupported || after.Stateless.Tools.State != CapabilityStateUnsupported {
		t.Fatalf("tools capability changed: before=%q after=%q", before.Stateless.Tools.State, after.Stateless.Tools.State)
	}
	if provider.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", provider.streamCalls)
	}
	if provider.calls != 0 {
		t.Fatalf("stateless Infer calls = %d, want 0", provider.calls)
	}
}
