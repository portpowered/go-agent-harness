package wire

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

func TestServiceRecorderRedactsConfiguredAndCredentialShapedValues(t *testing.T) {
	const credential = "browser-recorder-test-secret-20260913"
	events := make(chan browserconversation.BrowserEvent, 1)
	recorder, err := NewService().NewRecorder(browserconversation.RecordingRequest{
		Watch: func(context.Context) <-chan browserconversation.BrowserEvent { return events }, IncludeArguments: true, IncludeResults: true,
		RedactURLQuery: true, RedactURLFragment: true, Credentials: []string{credential},
	})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	recorder.Start(context.Background())
	events <- browserconversation.BrowserEvent{Type: "tool_responded", BrowserID: "browser-1", TargetID: "tab-1", Generation: 1, InvocationID: "invocation-1", ToolName: "lookup", Input: json.RawMessage(`{"token":"browser-recorder-test-secret-20260913"}`), Output: json.RawMessage(`{"message":"Authorization: Bearer browser-recorder-test-secret-20260913"}`)}
	close(events)
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(snapshot.Events))
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(encoded), credential) || !strings.Contains(string(encoded), "[redacted]") {
		t.Fatalf("snapshot redaction = %s", encoded)
	}
	if snapshot.Artifact == nil || !strings.Contains(string(snapshot.Artifact.Data), `"browser.invocation.completed"`) || strings.Contains(string(snapshot.Artifact.Data), credential) {
		t.Fatalf("canonical artifact = %#v, want redacted invocation evidence", snapshot.Artifact)
	}
	if err := snapshot.Artifact.Validate(); err != nil {
		t.Fatalf("canonical artifact validation: %v", err)
	}
}

func TestServiceRecorderEnforcesByteBound(t *testing.T) {
	events := make(chan browserconversation.BrowserEvent, 1)
	recorder, err := NewService().NewRecorder(browserconversation.RecordingRequest{Watch: func(context.Context) <-chan browserconversation.BrowserEvent { return events }, MaxBytes: 1})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	recorder.Start(context.Background())
	events <- browserconversation.BrowserEvent{Type: "browser.page.navigated", BrowserID: "browser-1"}
	close(events)
	if err := recorder.Close(); err == nil {
		t.Fatal("Close succeeded after recorder byte bound was exceeded")
	}
	snapshot, snapshotErr := recorder.Snapshot()
	if snapshotErr == nil {
		t.Fatal("Snapshot omitted recorder byte-bound error")
	}
	if len(snapshot.Events) != 0 {
		t.Fatalf("events retained after byte-bound rejection = %d, want 0", len(snapshot.Events))
	}
}

func TestServiceRecorderBoundsRawPayloadBeforeRedaction(t *testing.T) {
	events := make(chan browserconversation.BrowserEvent, 1)
	recorder, err := NewService().NewRecorder(browserconversation.RecordingRequest{
		Watch:            func(context.Context) <-chan browserconversation.BrowserEvent { return events },
		IncludeArguments: true, MaxBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	recorder.Start(context.Background())
	events <- browserconversation.BrowserEvent{
		Type: "tool_invoked", BrowserID: "browser-1", TargetID: "tab-1", InvocationID: "invocation-1",
		Input: json.RawMessage(`{"api_key":"` + strings.Repeat("x", 4096) + `"}`),
	}
	close(events)
	if err := recorder.Close(); err == nil {
		t.Fatal("Close succeeded after oversized raw arguments exceeded the recorder byte bound")
	}
	snapshot, err := recorder.Snapshot()
	if err == nil || len(snapshot.Events) != 0 {
		t.Fatalf("Snapshot = %+v, error = %v; want bound failure and no retained events", snapshot, err)
	}
}

func TestServiceRecorderDoesNotRetainExcludedRawPayload(t *testing.T) {
	events := make(chan browserconversation.BrowserEvent, 1)
	recorder, err := NewService().NewRecorder(browserconversation.RecordingRequest{
		Watch:    func(context.Context) <-chan browserconversation.BrowserEvent { return events },
		MaxBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	recorder.Start(context.Background())
	events <- browserconversation.BrowserEvent{
		Type: "tool_invoked", BrowserID: "browser-1", TargetID: "tab-1", InvocationID: "invocation-1",
		Input: json.RawMessage(`{"value":"` + strings.Repeat("excluded", 2048) + `"}`),
	}
	close(events)
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close with excluded payload: %v", err)
	}
	snapshot, err := recorder.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(encoded), "excluded") || strings.Contains(string(snapshot.Artifact.Data), "excluded") {
		t.Fatalf("excluded arguments appeared in retained recording: %s / %s", encoded, snapshot.Artifact.Data)
	}
}

func TestServiceRecorderBoundsRawToolDescriptorFieldsBeforeRetention(t *testing.T) {
	tests := []struct {
		name string
		tool browserconversation.BrowserToolDescriptor
	}{
		{name: "reference", tool: browserconversation.BrowserToolDescriptor{Ref: strings.Repeat("r", 1<<20), Name: "lookup"}},
		{name: "frame ID", tool: browserconversation.BrowserToolDescriptor{FrameID: strings.Repeat("f", 1<<20), Name: "lookup"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := make(chan browserconversation.BrowserEvent, 1)
			recorder, err := NewService().NewRecorder(browserconversation.RecordingRequest{
				Watch:    func(context.Context) <-chan browserconversation.BrowserEvent { return events },
				MaxBytes: 1024,
			})
			if err != nil {
				t.Fatalf("NewRecorder: %v", err)
			}
			recorder.Start(context.Background())
			events <- browserconversation.BrowserEvent{
				Type: "tools_added", BrowserID: "browser-1", TargetID: "tab-1",
				Tools: []browserconversation.BrowserToolDescriptor{test.tool},
			}
			close(events)
			if err := recorder.Close(); err == nil {
				t.Fatal("Close succeeded after oversized raw tool descriptor data exceeded the recorder byte bound")
			}
			snapshot, err := recorder.Snapshot()
			if err == nil || len(snapshot.Events) != 0 {
				t.Fatalf("Snapshot = %+v, error = %v; want bound failure and no retained events", snapshot, err)
			}
		})
	}
}

func TestServiceRecorderCloseIsBounded(t *testing.T) {
	events := make(chan browserconversation.BrowserEvent)
	recorder, err := NewService().NewRecorder(browserconversation.RecordingRequest{Watch: func(context.Context) <-chan browserconversation.BrowserEvent { return events }})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	recorder.Start(context.Background())
	started := time.Now()
	if err := recorder.Close(); err == nil {
		t.Fatal("Close succeeded while watcher ignored cancellation")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close took %s, want bounded shutdown", elapsed)
	}
	close(events)
	if err := recorder.Close(); err == nil {
		t.Fatal("second Close omitted bounded-shutdown error")
	}
}
