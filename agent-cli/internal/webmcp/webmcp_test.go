package webmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Compile-time fake assertions keep every public seam implementable without a
// browser dependency. These fakes intentionally contain no behavior beyond
// satisfying the frozen method sets.
type fakeDiscoverer struct{}

func (fakeDiscoverer) Discover(context.Context, DiscoverOptions) ([]BrowserCandidate, error) {
	return nil, nil
}

type fakeDevToolsCatalog struct{}

func (fakeDevToolsCatalog) Version(context.Context, BrowserCandidate) (BrowserVersion, error) {
	return BrowserVersion{}, nil
}
func (fakeDevToolsCatalog) ListTargets(context.Context, BrowserCandidate) ([]Target, error) {
	return nil, nil
}

type fakeBrowserRuntime struct{}

func (fakeBrowserRuntime) Open(context.Context, BrowserCandidate) (BrowserHandle, error) {
	return fakeBrowserHandle{}, nil
}

type fakeBrowserHandle struct{}

func (fakeBrowserHandle) Candidate() BrowserCandidate                   { return BrowserCandidate{} }
func (fakeBrowserHandle) ListTargets(context.Context) ([]Target, error) { return nil, nil }
func (fakeBrowserHandle) Activate(context.Context, TargetID) error      { return nil }
func (fakeBrowserHandle) Attach(context.Context, TargetID, TargetOwnership) (TargetSession, error) {
	return fakeTargetSession{}, nil
}
func (fakeBrowserHandle) Close() error { return nil }

type fakeTargetSession struct{}

func (fakeTargetSession) Context() PageContext               { return PageContext{} }
func (fakeTargetSession) Ownership() TargetOwnership         { return TargetOwnershipExternal }
func (fakeTargetSession) EnableWebMCP(context.Context) error { return nil }
func (fakeTargetSession) Events() <-chan BrowserEvent        { return nil }
func (fakeTargetSession) InvokeWebMCP(context.Context, FrameID, string, json.RawMessage) (InvocationID, error) {
	return "", nil
}
func (fakeTargetSession) CancelWebMCP(context.Context, InvocationID) error { return nil }
func (fakeTargetSession) Done() <-chan struct{}                            { return nil }
func (fakeTargetSession) Err() error                                       { return nil }
func (fakeTargetSession) Close() error                                     { return nil }

type fakeBroker struct{}

func (fakeBroker) Discover(context.Context, DiscoverOptions) ([]BrowserCandidate, error) {
	return nil, nil
}
func (fakeBroker) ListTargets(context.Context, BrowserSelector) ([]Target, error) {
	return nil, nil
}
func (fakeBroker) Select(context.Context, TargetSelector) (PageContext, error) {
	return PageContext{}, nil
}
func (fakeBroker) Selected(context.Context) (PageContext, error) { return PageContext{}, nil }
func (fakeBroker) ListTools(context.Context, ListToolsOptions) (ToolCatalogSnapshot, error) {
	return ToolCatalogSnapshot{}, nil
}
func (fakeBroker) Invoke(context.Context, InvokeRequest) (InvokeResult, error) {
	return InvokeResult{}, nil
}
func (fakeBroker) Cancel(context.Context, CancelRequest) error { return nil }
func (fakeBroker) Watch(context.Context) <-chan BrokerEvent    { return nil }
func (fakeBroker) Close() error                                { return nil }

type fakeSynchronizer struct{}

func (fakeSynchronizer) WaitReady(context.Context, <-chan CatalogEvent) (CatalogSyncResult, error) {
	return CatalogSyncResult{}, nil
}

type fakeRecorder struct{}

func (fakeRecorder) Record(BrowserEvent) error { return nil }
func (fakeRecorder) Flush() error              { return nil }

type fakeClock struct{}

func (fakeClock) Now() time.Time { return time.Unix(0, 0) }

type fakeIDs struct{}

func (fakeIDs) NewID(string) string { return "fake-id" }

var (
	_ BrowserDiscoverer   = fakeDiscoverer{}
	_ DevToolsCatalog     = fakeDevToolsCatalog{}
	_ BrowserRuntime      = fakeBrowserRuntime{}
	_ BrowserHandle       = fakeBrowserHandle{}
	_ TargetSession       = fakeTargetSession{}
	_ Broker              = fakeBroker{}
	_ CatalogSynchronizer = fakeSynchronizer{}
	_ SemanticRecorder    = fakeRecorder{}
	_ Clock               = fakeClock{}
	_ IDGenerator         = fakeIDs{}
)

func TestToolResultEnvelopeRoundTripsSuccess(t *testing.T) {
	input := json.RawMessage(`{"browser_id":"chrome-local-1","output":{"large":9007199254740993,"items":[1,2]}}`)
	want, err := NewToolSuccessEnvelope(input)
	if err != nil {
		t.Fatalf("NewToolSuccessEnvelope() error = %v", err)
	}
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var got ToolResultEnvelope
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.Version != ToolResultVersion || !got.OK || got.Error != nil {
		t.Fatalf("decoded envelope metadata = %#v", got)
	}
	if !bytes.Equal(got.Data, input) {
		t.Fatalf("decoded data = %s, want %s", got.Data, input)
	}
}

func TestToolResultEnvelopeRoundTripsError(t *testing.T) {
	errorValue := NewBrokerError(
		ErrorCodeInvalidToolInput,
		"Input does not match the selected tool schema.",
		true,
		json.RawMessage(`{"tool_ref":"wmcp1_example","input_schema":{"type":"object"},"issues":[]}`),
	)
	want := NewToolErrorEnvelope(errorValue)
	wire, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var got ToolResultEnvelope
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.Version != ToolResultVersion || got.OK || got.Data == nil || string(got.Data) != "null" {
		t.Fatalf("decoded envelope shape = %#v", got)
	}
	if got.Error == nil || got.Error.Code != ErrorCodeInvalidToolInput || !got.Error.Retryable {
		t.Fatalf("decoded error = %#v", got.Error)
	}
	if !bytes.Equal(got.Error.Details, errorValue.Details) {
		t.Fatalf("decoded details = %s, want %s", got.Error.Details, errorValue.Details)
	}
}

func TestToolResultEnvelopeRejectsUnknownVersion(t *testing.T) {
	var got ToolResultEnvelope
	err := json.Unmarshal([]byte(`{"version":"webmcp.tool-result.v2","ok":true,"data":{},"error":null}`), &got)
	if !errors.Is(err, ErrUnknownEnvelopeVersion) {
		t.Fatalf("json.Unmarshal() error = %v, want ErrUnknownEnvelopeVersion", err)
	}
}

func TestToolResultEnvelopeRejectsUnknownTopLevelField(t *testing.T) {
	var got ToolResultEnvelope
	err := json.Unmarshal([]byte(`{"version":"webmcp.tool-result.v1","ok":true,"data":{},"error":null,"extra":true}`), &got)
	if !errors.Is(err, ErrInvalidToolResultEnvelope) {
		t.Fatalf("json.Unmarshal() error = %v, want ErrInvalidToolResultEnvelope", err)
	}
}

func TestNoopBrokerDoesNotDialAndReturnsClassifiedDisabledError(t *testing.T) {
	_, err := (NoopBroker{}).Discover(context.Background(), DiscoverOptions{})
	var classified BrokerError
	if !errors.As(err, &classified) {
		t.Fatalf("Discover() error = %v, want BrokerError", err)
	}
	if classified.Code != ErrorCodeWebMCPDisabled {
		t.Fatalf("Discover() code = %q, want %q", classified.Code, ErrorCodeWebMCPDisabled)
	}
}
