package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func TestSessionToolExecutorScreenFailureUsesTypedEnvelope(t *testing.T) {
	var diagnostic SessionToolDiagnostic
	var diagnosticCalls int
	response, err := newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, &cliTools.ScreenCaptureError{
			State:     cliTools.ScreenCaptureDenied,
			Operation: "show",
			Reason:    "screen recording permission denied",
		}
	}), nil, 0, nil, nil, SessionToolDiagnosticFunc(func(got SessionToolDiagnostic) {
		diagnostic = got
		diagnosticCalls++
	})).Execute(context.Background(), messages.ToolCall{ID: "screen-failure", Name: "show", Arguments: `{}`})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if len(response.ContentParts) != 0 || response.ToolCallID != "screen-failure" || response.Name != "show" {
		t.Fatalf("screen failure response = %#v, want correlated text-only result", response)
	}
	result, err := sight.Decode([]byte(response.Content))
	if err != nil {
		t.Fatalf("decode screen failure: %v", err)
	}
	if result.Status != sight.StatusError || result.Source != sight.SourceScreen || result.ErrorCode != cliTools.ScreenRecordingPermissionDeniedErrorCode || strings.TrimSpace(result.Error) == "" {
		t.Fatalf("screen failure result = %+v, want classified non-empty error", result)
	}
	for _, forbidden := range []string{
		"System Settings → Privacy & Security → Screen & System Audio Recording",
		"hosting application",
		"Tell the customer",
		"completely quit and restart",
		"macOS Sequoia",
		"monthly re-confirmation",
	} {
		if strings.Contains(result.Error, forbidden) {
			t.Errorf("session screen failure error %q contains operator-only text %q", result.Error, forbidden)
		}
	}
	if result.Error != "Screen sight is unavailable." {
		t.Fatalf("session screen failure error = %q, want concise customer-safe message", result.Error)
	}
	if diagnosticCalls != 1 || diagnostic.ToolCallID != "screen-failure" || diagnostic.ToolName != "show" || diagnostic.Source != sight.SourceScreen || diagnostic.ErrorCode != cliTools.ScreenRecordingPermissionDeniedErrorCode || diagnostic.Error == nil || !strings.Contains(diagnostic.Error.Error(), "System Settings → Privacy & Security → Screen & System Audio Recording") {
		t.Fatalf("operator diagnostic = %#v, calls=%d, want original typed denial with remediation", diagnostic, diagnosticCalls)
	}
}

func TestDirectScreenToolErrorResultRetainsOperatorGuidance(t *testing.T) {
	err := &cliTools.ScreenCaptureError{
		State:     cliTools.ScreenCaptureDenied,
		Operation: "show",
		Reason:    "screen recording permission denied",
	}
	result, decodeErr := sight.Decode([]byte(cliTools.ScreenToolErrorResult(err)))
	if decodeErr != nil {
		t.Fatalf("decode direct screen error: %v", decodeErr)
	}
	for _, want := range []string{
		"System Settings → Privacy & Security → Screen & System Audio Recording",
		"hosting application",
		"Tell the customer",
		"completely quit and restart",
	} {
		if !strings.Contains(result.Error, want) {
			t.Errorf("direct screen error %q does not contain operator guidance %q", result.Error, want)
		}
	}
}

func TestComposedScreenToolDeliversOneProjectionAndRemainsUsable(t *testing.T) {
	surface := &sessionSightDisplaySurface{
		capability: runtimeTools.DisplayCapability{State: runtimeTools.ScreenCaptureGranted, Available: true, DisplayCount: 1},
		frame:      image.NewRGBA(image.Rect(0, 0, 2, 2)),
	}
	capability, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		WorkDir:              t.TempDir(),
		DisplaySurface:       surface,
		DisplayCapability:    surface.capability,
		DisplayCapabilitySet: true,
		Selections:           screenOnlyToolSelections(),
		UseDefaultTool:       true,
	})
	if err != nil {
		t.Fatalf("resolve screen tool: %v", err)
	}
	executor := capability.Executor
	call := messages.ToolCall{ID: "screen-composed-1", Name: runtimeTools.ScreenToolID, Arguments: `{}`}
	response, err := newSessionToolExecutor(executor).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("composed screen execute: %v", err)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name || len(response.ContentParts) != 2 {
		t.Fatalf("composed screen response = %#v, want correlated metadata plus image", response)
	}
	result, err := sight.Decode([]byte(response.Content))
	if err != nil || result.Status != sight.StatusSuccess || result.Source != sight.SourceScreen {
		t.Fatalf("composed screen result = %+v, err = %v", result, err)
	}
	imagePart, ok := response.ContentParts[1].(messages.ImagePart)
	if !ok || imagePart.MediaType != "image/jpeg" || len(imagePart.Bytes) == 0 {
		t.Fatalf("composed screen image = %#v", response.ContentParts[1])
	}

	// A second call through the same adapter proves a capture result does not
	// poison the session tool path for the following turn.
	second := call
	second.ID = "screen-composed-2"
	response, err = newSessionToolExecutor(executor).Execute(context.Background(), second)
	if err != nil || len(response.ContentParts) != 2 || surface.captures != 2 {
		t.Fatalf("later composed screen response = %#v, err = %v, captures = %d", response, err, surface.captures)
	}
}

func TestComposedPageSightTimeoutDoesNotRecheckHostPermission(t *testing.T) {
	static := &sessionPageSightTimeoutExecutor{}
	broker := &sessionPageSightTimeoutExecutor{}
	capability, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		Executor:    static,
		Definitions: []messages.ToolDefinition{{Name: runtimeTools.ScreenToolID}},
		Browser: &runtimeTools.BrowserSurface{
			Executor:    broker,
			Definitions: []messages.ToolDefinition{{Name: cliTools.PageSightToolID}},
		},
	})
	if err != nil {
		t.Fatalf("compose page sight surface: %v", err)
	}

	call := messages.ToolCall{ID: "page-timeout", Name: runtimeTools.ScreenToolID, Arguments: `{}`}
	response, err := newSessionToolExecutorWithTimeout(capability.Executor, 10*time.Millisecond).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("page sight timeout returned Go error: %v", err)
	}
	result, decodeErr := sight.Decode([]byte(response.Content))
	if decodeErr != nil || result.Source != sight.SourceBrowserPage || result.ErrorCode != "page_sight_unavailable" {
		t.Fatalf("page timeout result = %+v, err = %v, want browser-page failure", result, decodeErr)
	}
	if static.calls != 0 || static.rechecks != 0 || broker.rechecks != 0 {
		t.Fatalf("page timeout touched host backend: static_calls=%d static_rechecks=%d broker_rechecks=%d", static.calls, static.rechecks, broker.rechecks)
	}
}

func TestRunAgentLoopSession_PageSightUsesOneSourceForSuccessiveQuestions(t *testing.T) {
	pageMetadata := `{"version":2,"status":"success","source":"browser_page","mime_type":"image/png","byte_length":4,"width":1,"height":1,"sha256":"` + strings.Repeat("0", 64) + `","typed_projection":"input_image"}`
	static := &sessionSightPageExecutor{}
	broker := &sessionSightPageExecutor{
		response: messages.ToolCallResponse{
			Content: pageMetadata,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: pageMetadata},
				messages.ImagePart{Bytes: []byte{0x89, 'P', 'N', 'G'}, MediaType: "image/png"},
			},
		},
	}
	capability, err := runtimeToolsWire.NewService().Resolve(context.Background(), runtimeTools.Request{
		Executor:    static,
		Definitions: []messages.ToolDefinition{{Name: runtimeTools.ScreenToolID, Description: "legacy host capture"}},
		Browser: &runtimeTools.BrowserSurface{
			Executor:    broker,
			Definitions: []messages.ToolDefinition{{Name: cliTools.PageSightToolID, Description: "selected page capture"}},
		},
	})
	if err != nil {
		t.Fatalf("compose page sight surface: %v", err)
	}

	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(
		out,
		"page sight continuation",
		"",
		scriptedTurn{events: toolCallEvents("broad-page-call", runtimeTools.ScreenToolID, `{}`)},
		scriptedTurn{events: toolCallEvents("literal-page-call", cliTools.PageSightToolID, `{}`), after: `"source":"browser_page"`},
	)
	if err := runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:     2 * time.Second,
		WaitForClose:    true,
		ToolExecutor:    capability.Executor,
		ToolDefinitions: capability.Definitions,
	}); err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if static.calls != 0 || static.rechecks != 0 {
		t.Fatalf("host display side effects = calls:%d rechecks:%d, want zero", static.calls, static.rechecks)
	}
	if broker.calls != 2 || len(broker.names) != 2 || broker.names[0] != cliTools.PageSightToolID || broker.names[1] != cliTools.PageSightToolID {
		t.Fatalf("page capture calls = %d names=%#v, want two show_page calls", broker.calls, broker.names)
	}
	if !strings.Contains(out.String(), `"source":"browser_page"`) || !strings.Contains(out.String(), "page sight continuation") {
		t.Fatalf("session output = %q, want page source and assistant continuation", out.String())
	}
}

func TestSessionDirectoryRecordingPersistsScreenCaptureEvidence(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 2, 1))); err != nil {
		t.Fatalf("encode screen fixture: %v", err)
	}
	result, err := sight.NewSuccess(sight.SourceScreen, "image/png", pixels.Bytes(), 2, 1)
	if err != nil {
		t.Fatalf("create screen result: %v", err)
	}
	encoded, err := sight.Encode(result)
	if err != nil {
		t.Fatalf("encode screen result: %v", err)
	}
	imageBytes := append([]byte(nil), pixels.Bytes()...)
	call := messages.ToolCall{ID: "screen-call-1", Name: "show", Arguments: `{"action":"screenshot"}`}
	recording := newSessionDirectoryRecording(filepath.Join(t.TempDir(), "screen-recording"), sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{ModelCatalog: testModelCatalog(), Model: "gpt-realtime"})
	writeSyntheticRecordingTranscript(t, recording, "client", "agent")
	recording.observeToolCall(call)
	recording.observeToolResult(call, messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    string(encoded),
		ContentParts: []messages.ContentPart{
			messages.TextPart{Text: string(encoded)},
			messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
		},
	}, false)
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize screen recording: %v", err)
	}

	var manifest transcript.RecordingManifest
	manifestBytes, err := os.ReadFile(filepath.Join(recording.destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	digest := sha256.Sum256(imageBytes)
	var screenshotPath string
	for _, artifact := range manifest.Artifacts {
		if strings.HasPrefix(artifact.Path, "screenshots/") {
			screenshotPath = artifact.Path
			if artifact.SHA256 != result.SHA256 || artifact.SHA256 != stringDigest(digest) {
				t.Fatalf("screenshot manifest artifact = %+v, want capture digest", artifact)
			}
		}
	}
	if screenshotPath == "" {
		t.Fatalf("manifest artifacts = %#v, want screenshot artifact", manifest.Artifacts)
	}
	stored, err := os.ReadFile(filepath.Join(recording.destination, filepath.FromSlash(screenshotPath)))
	if err != nil {
		t.Fatalf("read screenshot artifact: %v", err)
	}
	if !bytes.Equal(stored, imageBytes) {
		t.Fatalf("stored screenshot bytes differ from the projected image")
	}

	logBytes, err := os.ReadFile(filepath.Join(recording.destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if !bytes.Contains(logBytes, []byte(`"image"`)) || !bytes.Contains(logBytes, []byte(screenshotPath)) || !bytes.Contains(logBytes, []byte(`"source":"screen"`)) {
		t.Fatalf("session log = %s, want correlated screenshot evidence", logBytes)
	}
}

type sessionSightDisplaySurface struct {
	capability runtimeTools.DisplayCapability
	frame      *image.RGBA
	captures   int
}

type sessionPageSightTimeoutExecutor struct {
	calls    int
	rechecks int
}

type sessionSightPageExecutor struct {
	response messages.ToolCallResponse
	calls    int
	rechecks int
	names    []string
}

func (e *sessionSightPageExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	e.names = append(e.names, call.Name)
	response := e.response
	response.ToolCallID = call.ID
	response.Name = call.Name
	return response, nil
}

func (e *sessionSightPageExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	return true
}

func (e *sessionSightPageExecutor) RecheckScreenRecordingPermission(context.Context) (runtimeTools.DisplayPermission, error) {
	e.rechecks++
	return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionGranted}, nil
}

func (e *sessionPageSightTimeoutExecutor) Execute(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	<-ctx.Done()
	return messages.ToolCallResponse{}, ctx.Err()
}

func (e *sessionPageSightTimeoutExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	return true
}

func (e *sessionPageSightTimeoutExecutor) RecheckScreenRecordingPermission(context.Context) (runtimeTools.DisplayPermission, error) {
	e.rechecks++
	return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionDenied}, nil
}

func (s *sessionSightDisplaySurface) Probe(context.Context) (runtimeTools.DisplayCapability, error) {
	return s.capability, nil
}

func (s *sessionSightDisplaySurface) DisplayCount(context.Context) (int, error) {
	return s.capability.DisplayCount, nil
}

func (s *sessionSightDisplaySurface) Bounds(context.Context, int) (image.Rectangle, error) {
	return s.frame.Bounds(), nil
}

func (s *sessionSightDisplaySurface) Capture(context.Context, image.Rectangle) (*image.RGBA, error) {
	s.captures++
	return s.frame, nil
}

func stringDigest(digest [sha256.Size]byte) string {
	return hex.EncodeToString(digest[:])
}

func screenOnlyToolSelections() []runtimeTools.ToolSelection {
	selections := make([]runtimeTools.ToolSelection, 0, 13)
	for _, id := range []string{
		"exec", "read_file", runtimeTools.ReadImageToolID, "write_file", "edit_file",
		"append_file", "list_dir", "web_fetch", "web_search", runtimeTools.ScreenToolID,
		"mouse", "load_skill", "sleep",
	} {
		selections = append(selections, runtimeTools.ToolSelection{ID: id, Enabled: id == runtimeTools.ScreenToolID})
	}
	return selections
}
