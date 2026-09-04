package services

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestSessionBrowserRecordingCapturesRedactedInvocationEvidence(t *testing.T) {
	const credential = "session-browser-recording-credential-20260831"
	cases := []struct {
		name             string
		includeArguments bool
		includeResults   bool
		wantInput        bool
		wantOutput       bool
	}{
		{name: "included", includeArguments: true, includeResults: true, wantInput: true, wantOutput: true},
		{name: "omitted", includeArguments: false, includeResults: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			events := make(chan webmcp.BrowserEvent, 2)
			loaded := &config.Config{Browser: config.DefaultBrowserConfig()}
			loaded.Browser.Recording.Enabled = true
			loaded.Browser.Recording.IncludeArguments = testCase.includeArguments
			loaded.Browser.Recording.IncludeResults = testCase.includeResults
			recording := newSessionBrowserRecording(SessionRunOptions{
				APIKey:            credential,
				LoadedConfig:      loaded,
				BrowserEventWatch: func(context.Context) <-chan webmcp.BrowserEvent { return events },
			}, sessionRuntimePlan{})
			if recording == nil {
				t.Fatal("recording is nil")
			}
			recording.start(context.Background())
			events <- webmcp.BrowserEvent{
				Type:         webmcp.EventToolInvoked,
				BrowserID:    "browser-1",
				TargetID:     "tab-1",
				Generation:   1,
				FrameID:      "frame-1",
				InvocationID: "inv-1",
				ToolName:     "lookup",
				Input:        json.RawMessage(`{"url":"https://fixture.test/path?token=` + credential + `#private"}`),
			}
			events <- webmcp.BrowserEvent{
				Type:         webmcp.EventToolResponded,
				BrowserID:    "browser-1",
				TargetID:     "tab-1",
				Generation:   1,
				InvocationID: "inv-1",
				Status:       "completed",
				Output:       json.RawMessage(`{"url":"https://fixture.test/result?secret=remove#private","value":24}`),
			}
			close(events)
			recording.stop()

			artifact, err := recording.artifact()
			if err != nil {
				t.Fatalf("recording artifact: %v", err)
			}
			if artifact == nil {
				t.Fatal("recording artifact is nil")
			}
			if strings.Contains(string(artifact.Data), credential) || strings.Contains(string(artifact.Data), "token=") {
				t.Fatalf("artifact retained configured credential or URL query: %s", artifact.Data)
			}
			decoded, err := testkit.ValidateEventStream(artifact.Data)
			if err != nil {
				t.Fatalf("validate artifact: %v", err)
			}
			if len(decoded) != 3 || decoded[0].Type != testkit.EventBrowserInvocationCreated || decoded[1].Type != testkit.EventBrowserInvocationDispatched || decoded[2].Type != testkit.EventBrowserInvocationCompleted {
				t.Fatalf("event lifecycle = %#v, want created/dispatched/completed", decoded)
			}
			var dispatched, completed map[string]json.RawMessage
			if err := json.Unmarshal(decoded[1].Payload, &dispatched); err != nil {
				t.Fatalf("decode dispatched payload: %v", err)
			}
			if err := json.Unmarshal(decoded[2].Payload, &completed); err != nil {
				t.Fatalf("decode completed payload: %v", err)
			}
			if _, present := dispatched["input"]; present != testCase.wantInput {
				t.Fatalf("input presence = %t, want %t", present, testCase.wantInput)
			}
			if _, present := completed["output"]; present != testCase.wantOutput {
				t.Fatalf("output presence = %t, want %t", present, testCase.wantOutput)
			}
		})
	}
}

func TestSessionDirectoryRecordingPersistsBrowserArtifact(t *testing.T) {
	const credential = "session-browser-bundle-credential-20260831"
	events := make(chan webmcp.BrowserEvent, 1)
	loaded := &config.Config{Browser: config.DefaultBrowserConfig()}
	loaded.Browser.Recording.Enabled = true
	recording := newSessionDirectoryRecording(filepath.Join(t.TempDir(), "recording"), sessionRuntimePlan{}, SessionRunOptions{
		APIKey:       credential,
		LoadedConfig: loaded,
		BrowserEventWatch: func(context.Context) <-chan webmcp.BrowserEvent {
			return events
		},
	})
	recording.client.WriteString("client")
	recording.agent.WriteString("agent")
	recording.browser.start(context.Background())
	events <- webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		BrowserID:    "browser-1",
		TargetID:     "tab-1",
		Generation:   1,
		InvocationID: "inv-1",
		ToolName:     "lookup",
		Input:        json.RawMessage(`{"token":"` + credential + `"}`),
	}
	events <- webmcp.BrowserEvent{
		Type:         webmcp.EventToolResponded,
		BrowserID:    "browser-1",
		TargetID:     "tab-1",
		Generation:   1,
		InvocationID: "inv-1",
		Status:       "completed",
		Output:       json.RawMessage(`{"value":24}`),
	}
	close(events)
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize browser recording bundle: %v", err)
	}

	artifact, err := os.ReadFile(filepath.Join(recording.destination, transcript.BrowserArtifactDefaultPath))
	if err != nil {
		t.Fatalf("read browser artifact: %v", err)
	}
	if strings.Contains(string(artifact), credential) {
		t.Fatalf("browser artifact leaked recording credential: %s", artifact)
	}
	if !strings.Contains(string(artifact), `"browser.invocation.dispatched"`) || !strings.Contains(string(artifact), `"browser.invocation.completed"`) {
		t.Fatalf("browser artifact = %s, want dispatched and completed evidence", artifact)
	}
	manifest, err := os.ReadFile(filepath.Join(recording.destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read recording manifest: %v", err)
	}
	if !strings.Contains(string(manifest), `"browser"`) || !strings.Contains(string(manifest), `"browser.events.jsonl"`) {
		t.Fatalf("recording manifest = %s, want browser artifact metadata", manifest)
	}
}

func TestSessionBrowserRecordingDefaultsEmptyNavigationReason(t *testing.T) {
	inputs, err := sessionBrowserEventInputs(webmcp.BrowserEvent{
		Type:               webmcp.EventPageNavigated,
		BrowserID:          "browser-1",
		TargetID:           "tab-1",
		PreviousGeneration: 1,
		Generation:         2,
	}, false, false)
	if err != nil {
		t.Fatalf("sessionBrowserEventInputs(): %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %#v", inputs)
	}
	recorder, err := testkit.NewRecorder(io.Discard)
	if err != nil {
		t.Fatalf("NewRecorder(): %v", err)
	}
	recorded, err := recorder.Record(inputs[0])
	if err != nil {
		t.Fatalf("Record(): %v", err)
	}
	if !strings.Contains(string(recorded.Payload), `"reason":"navigation"`) {
		t.Fatalf("payload = %s", recorded.Payload)
	}
}
