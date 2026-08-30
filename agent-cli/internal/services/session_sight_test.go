package services

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

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func TestSessionToolExecutorScreenFailureUsesTypedEnvelope(t *testing.T) {
	response, err := newSessionToolExecutor(sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{}, &cliTools.ScreenCaptureError{
			State:     cliTools.ScreenCaptureDenied,
			Operation: "show",
			Reason:    "screen recording permission denied",
		}
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
	for _, want := range []string{
		"System Settings → Privacy & Security → Screen & System Audio Recording",
		"hosting application",
		"Tell the customer",
		"completely quit and restart",
		"macOS Sequoia",
		"monthly re-confirmation",
	} {
		if !strings.Contains(result.Error, want) {
			t.Errorf("screen failure error %q does not contain %q", result.Error, want)
		}
	}
}

func TestComposedScreenToolDeliversOneProjectionAndRemainsUsable(t *testing.T) {
	surface := &sessionSightDisplaySurface{
		capability: cliTools.UsableDisplayCapability(1),
		frame:      image.NewRGBA(image.Rect(0, 0, 2, 2)),
	}
	registry := cliTools.NewEmptyToolRegistry()
	if err := registry.Register(cliTools.NewScreenToolWithDisplaySurface(surface)); err != nil {
		t.Fatalf("register screen tool: %v", err)
	}
	executor := cliTools.NewRegistryExecutor(registry)
	call := messages.ToolCall{ID: "screen-composed-1", Name: cliTools.ScreenToolID, Arguments: `{}`}
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
	recording := newSessionDirectoryRecording(filepath.Join(t.TempDir(), "screen-recording"), sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{Model: "gpt-realtime"})
	recording.client.WriteString("client")
	recording.agent.WriteString("agent")
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
	capability cliTools.DisplayCapability
	frame      *image.RGBA
	captures   int
}

func (s *sessionSightDisplaySurface) Probe(context.Context) (cliTools.DisplayCapability, error) {
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
