package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestPlanSessionRuntime_BindsReadImageWithSessionScopedPreparation(t *testing.T) {
	sharedRegistry := tools.NewToolRegistryFromConfig(nil)
	sharedExecutor := tools.NewRegistryExecutor(sharedRegistry)
	definitions := sharedRegistry.ToAgentLoopDefs()

	dirOne := t.TempDir()
	dirTwo := t.TempDir()
	pathOne := writeSessionReadImagePNG(t, dirOne, "one.png", color.RGBA{R: 255, A: 255})
	pathTwo := writeSessionReadImagePNG(t, dirTwo, "two.png", color.RGBA{B: 255, A: 255})

	planOne := planReadImageTestSession(t, dirOne, sharedExecutor, definitions)
	planTwo := planReadImageTestSession(t, dirTwo, sharedExecutor, definitions)

	responseOne := executePlannedReadImage(t, planOne, pathOne, "call-one")
	responseTwo := executePlannedReadImage(t, planTwo, pathTwo, "call-two")
	assertReadImageResponse(t, responseOne, "call-one", mustReadSessionReadImage(t, pathOne), "image/png")
	assertReadImageResponse(t, responseTwo, "call-two", mustReadSessionReadImage(t, pathTwo), "image/png")

	// Constructing the second plan must not overwrite the first plan's private
	// callback. Calling the first executor again proves both session bindings
	// remain usable concurrently from the same process-wide registry.
	responseOneAgain := executePlannedReadImage(t, planOne, pathOne, "call-one-again")
	assertReadImageResponse(t, responseOneAgain, "call-one-again", mustReadSessionReadImage(t, pathOne), "image/png")
}

func TestRunAgentLoopSession_ReadImageResultReachesNextModelTurn(t *testing.T) {
	dir := t.TempDir()
	imagePath := writeSessionReadImagePNG(t, dir, "loop.png", color.RGBA{G: 255, A: 255})
	registry := tools.NewToolRegistryFromConfig(nil)
	executor := tools.NewRegistryExecutor(registry)
	plan := planReadImageTestSession(t, dir, executor, registry.ToAgentLoopDefs())
	arguments, err := json.Marshal(map[string]string{"path": imagePath})
	if err != nil {
		t.Fatal(err)
	}

	out := newSignalingBuffer()
	imageReady := make(chan struct{})
	inferencer := &readImageResultGatedInferencer{
		ready:     imageReady,
		callID:    "image-call",
		arguments: string(arguments),
	}
	var mu sync.Mutex
	var observed []messages.StreamMessage
	var imageReadyOnce sync.Once
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.streamObserver = func(event messages.StreamMessage) {
		mu.Lock()
		observed = append(observed, event)
		mu.Unlock()
		if event.Type == messages.StreamTypeImageEnd && event.ToolCallId == "image-call" {
			imageReadyOnce.Do(func() { close(imageReady) })
		}
	}

	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:          2 * time.Second,
		WaitForClose:         true,
		ToolExecutor:         plan.loop.ToolExecutor,
		ToolDefinitions:      plan.loop.ToolDefinitions,
		ToolExecutionTimeout: 2 * time.Second,
		observer:             observer,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "next model turn after image tool result") {
		t.Fatalf("session did not reach the next model turn:\n%s", out.String())
	}

	mu.Lock()
	defer mu.Unlock()
	var gotStart, gotDelta, gotEnd bool
	for _, event := range observed {
		if event.ToolCallId != "image-call" {
			continue
		}
		switch event.Type {
		case messages.StreamTypeImageStart:
			value, ok := event.Value.(*messages.ImageStartValue)
			gotStart = ok && value.MediaType == "image/png"
		case messages.StreamTypeImageDelta:
			value, ok := event.Value.(*messages.ImageDeltaValue)
			gotDelta = ok && bytes.Equal(value.Content, mustReadSessionReadImage(t, imagePath))
		case messages.StreamTypeImageEnd:
			gotEnd = true
		}
	}
	if !gotStart || !gotDelta || !gotEnd {
		t.Fatalf("observed image result events = %#v, want correlated start/delta/end with fixture bytes", observed)
	}
}

func TestReadImageSession_InvalidInputsReturnCorrelatedTextOnlyFailures(t *testing.T) {
	dir := t.TempDir()
	registry := tools.NewToolRegistryFromConfig(nil)
	sharedExecutor := tools.NewRegistryExecutor(registry)
	definitions := registry.ToAgentLoopDefs()

	valid := writeSessionReadImagePNG(t, dir, "valid.png", color.RGBA{R: 255, A: 255})
	missing := filepath.Join(dir, "missing.png")
	unreadable := filepath.Join(dir, "unreadable.png")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	textFile := writeReadImageTestFile(t, dir, "notes.txt", []byte("plain text"))
	audioFile := writeReadImageTestFile(t, dir, "sound.wav", []byte("audio payload"))
	videoFile := writeReadImageTestFile(t, dir, "movie.mp4", []byte("video payload"))
	empty := writeReadImageTestFile(t, dir, "empty.png", nil)
	corrupt := writeReadImageTestFile(t, dir, "corrupt.png", []byte("not an image"))
	unsupported := writeReadImageTestFile(t, dir, "unsupported.gif", []byte("GIF89a"))

	capabilityConfigDir := filepath.Join(dir, "text-only-config")
	if err := os.MkdirAll(capabilityConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capabilityConfigDir, "models.yaml"), []byte(`
models:
  - name: gpt-realtime
    providers: [openai]
    input_modalities: [text]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		args      map[string]any
		configDir string
		wantText  string
	}{
		{name: "missing path argument", args: nil, configDir: filepath.Join(dir, "missing-argument-config"), wantText: "path is required"},
		{name: "empty path", args: map[string]any{"path": "  "}, configDir: filepath.Join(dir, "empty-path-config"), wantText: "path must not be empty"},
		{name: "missing file", args: map[string]any{"path": missing}, configDir: filepath.Join(dir, "missing-file-config"), wantText: "is missing"},
		{name: "unreadable file", args: map[string]any{"path": unreadable}, configDir: filepath.Join(dir, "unreadable-config"), wantText: "cannot be read"},
		{name: "text file", args: map[string]any{"path": textFile}, configDir: filepath.Join(dir, "text-config"), wantText: "unsupported MIME type"},
		{name: "audio file", args: map[string]any{"path": audioFile}, configDir: filepath.Join(dir, "audio-config"), wantText: "unsupported MIME type"},
		{name: "video file", args: map[string]any{"path": videoFile}, configDir: filepath.Join(dir, "video-config"), wantText: "unsupported MIME type"},
		{name: "empty file", args: map[string]any{"path": empty}, configDir: filepath.Join(dir, "empty-file-config"), wantText: "is empty"},
		{name: "corrupt image", args: map[string]any{"path": corrupt}, configDir: filepath.Join(dir, "corrupt-config"), wantText: "not valid image/png"},
		{name: "unsupported image MIME", args: map[string]any{"path": unsupported}, configDir: filepath.Join(dir, "unsupported-config"), wantText: "unsupported MIME type"},
		{name: "image-incapable model", args: map[string]any{"path": valid}, configDir: capabilityConfigDir, wantText: "does not support image input"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planReadImageTestSession(t, tc.configDir, sharedExecutor, definitions)
			response := executeReadImageFailure(t, plan.loop.ToolExecutor, tc.args, "invalid-"+strings.ReplaceAll(tc.name, " ", "-"))
			assertReadImageFailure(t, response, "invalid-"+strings.ReplaceAll(tc.name, " ", "-"), tc.wantText)
		})
	}
}

func TestRunAgentLoopSession_ReadImageFailureKeepsSessionAlive(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.png")
	registry := tools.NewToolRegistryFromConfig(nil)
	sharedExecutor := tools.NewRegistryExecutor(registry)
	plan := planReadImageTestSession(t, filepath.Join(dir, "config"), sharedExecutor, registry.ToAgentLoopDefs())
	arguments, err := json.Marshal(map[string]string{"path": missing})
	if err != nil {
		t.Fatal(err)
	}

	const callID = "invalid-image-call"
	out := newSignalingBuffer()
	inferencer := newScriptedToolCallInferencer(
		out,
		"session continued after invalid image",
		"is missing",
		scriptedTurn{events: toolCallEvents(callID, tools.ReadImageToolID, string(arguments))},
	)
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	var observed []messages.StreamMessage
	var observedMu sync.Mutex
	observer.streamObserver = func(event messages.StreamMessage) {
		observedMu.Lock()
		observed = append(observed, event)
		observedMu.Unlock()
	}

	err = runAgentLoopSession(context.Background(), out, inferencer, sessionLoopOptions{
		MaxDuration:          2 * time.Second,
		WaitForClose:         true,
		ToolExecutor:         plan.loop.ToolExecutor,
		ToolDefinitions:      plan.loop.ToolDefinitions,
		ToolExecutionTimeout: 2 * time.Second,
		observer:             observer,
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "is missing") {
		t.Fatalf("missing-image failure did not reach the provider-visible result:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "session continued after invalid image") {
		t.Fatalf("session did not continue after invalid image:\n%s", out.String())
	}

	observedMu.Lock()
	defer observedMu.Unlock()
	for _, event := range observed {
		if event.ToolCallId != callID {
			continue
		}
		switch event.Type {
		case messages.StreamTypeImageStart, messages.StreamTypeImageDelta, messages.StreamTypeImageEnd:
			t.Fatalf("invalid image call emitted image event: %#v", event)
		}
	}
}

func executeReadImageFailure(t *testing.T, executor messages.ToolExecutor, args map[string]any, callID string) messages.ToolCallResponse {
	t.Helper()
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return mustExecuteSessionTool(t, executor, messages.ToolCall{ID: callID, Name: tools.ReadImageToolID, Arguments: string(arguments)})
}

func assertReadImageFailure(t *testing.T, response messages.ToolCallResponse, callID, wantText string) {
	t.Helper()
	if response.ToolCallID != callID || response.Name != tools.ReadImageToolID {
		t.Fatalf("failure response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, callID, tools.ReadImageToolID)
	}
	if response.Content == "" {
		t.Fatal("failure response is empty")
	}
	var result tools.ReadImageResult
	if err := json.Unmarshal([]byte(response.Content), &result); err != nil {
		t.Fatalf("failure response envelope = %q: %v", response.Content, err)
	}
	if result.Version != tools.ReadImageResultVersion || result.Status != tools.ReadImageResultStatusError || result.Error == "" || !strings.Contains(result.Error, wantText) {
		t.Fatalf("failure response envelope = %#v, want version %d error containing %q", result, tools.ReadImageResultVersion, wantText)
	}
	if result.MIMEType != "" || result.ByteLength != 0 || result.SHA256 != "" || result.TypedProjection != "" {
		t.Fatalf("failure response unexpectedly carried image metadata: %#v", result)
	}
	for _, part := range response.ContentParts {
		if _, ok := part.(messages.ImagePart); ok {
			t.Fatalf("failure response unexpectedly contained an image part: %#v", response.ContentParts)
		}
	}
}

type readImageResultGatedInferencer struct {
	ready     <-chan struct{}
	callID    string
	arguments string
}

func (i *readImageResultGatedInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newRoundTripSession()
	go func() {
		if !session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("read-image-session", "gpt-realtime"),
		}) {
			return
		}
		for _, event := range toolCallEvents(i.callID, tools.ReadImageToolID, i.arguments) {
			if !session.recv.Write(ctx, event) {
				return
			}
		}
		select {
		case <-i.ready:
		case <-ctx.Done():
			return
		}
		if !session.waitForSent(ctx, messages.StreamTypeResponseCreate) {
			return
		}
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageStart,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageStartValue(),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue("next model turn after image tool result"),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Role:  messages.RoleAssistant,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionClose,
			Value: messages.NewSessionCloseValue("read-image-session", "test complete"),
		})
	}()
	return session, nil
}

func planReadImageTestSession(t *testing.T, configDir string, executor messages.ToolExecutor, definitions []messages.ToolDefinition) sessionRuntimePlan {
	t.Helper()
	plan, err := planSessionRuntime(SessionRunOptions{
		ReplayPath:           "synthetic.json",
		Provider:             "openai",
		Model:                "gpt-realtime",
		ModelProvided:        true,
		ConfigDir:            configDir,
		SessionInferencer:    stubPlanSessionInferencer{},
		ToolExecutor:         executor,
		ToolDefinitions:      definitions,
		ToolExecutionTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("planSessionRuntime: %v", err)
	}
	return plan
}

func executePlannedReadImage(t *testing.T, plan sessionRuntimePlan, path, callID string) messages.ToolCallResponse {
	t.Helper()
	arguments, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return mustExecuteSessionTool(t, plan.loop.ToolExecutor, messages.ToolCall{ID: callID, Name: tools.ReadImageToolID, Arguments: string(arguments)})
}

func mustExecuteSessionTool(t *testing.T, executor messages.ToolExecutor, call messages.ToolCall) messages.ToolCallResponse {
	t.Helper()
	if executor == nil {
		t.Fatal("planned session tool executor is nil")
	}
	response, err := newSessionToolExecutorWithTimeout(executor, 2*time.Second).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("session tool execution: %v", err)
	}
	return response
}

func assertReadImageResponse(t *testing.T, response messages.ToolCallResponse, callID string, wantBytes []byte, wantMIME string) {
	t.Helper()
	if response.ToolCallID != callID || response.Name != tools.ReadImageToolID {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, callID, tools.ReadImageToolID)
	}
	if response.Content == "" || len(response.ContentParts) != 2 {
		t.Fatalf("response content = %q parts = %#v, want envelope plus image rich result", response.Content, response.ContentParts)
	}
	var result tools.ReadImageResult
	if err := json.Unmarshal([]byte(response.Content), &result); err != nil {
		t.Fatalf("response envelope = %q: %v", response.Content, err)
	}
	if result.Version != tools.ReadImageResultVersion || result.Status != tools.ReadImageResultStatusSuccess {
		t.Fatalf("response envelope = %#v, want version %d success", result, tools.ReadImageResultVersion)
	}
	if result.MIMEType != wantMIME || result.ByteLength != len(wantBytes) {
		t.Fatalf("response envelope metadata = %#v, want %s and %d bytes", result, wantMIME, len(wantBytes))
	}
	digest := sha256.Sum256(wantBytes)
	if result.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("response envelope sha256 = %q, want %q", result.SHA256, hex.EncodeToString(digest[:]))
	}
	if result.TypedProjection != tools.ReadImageResultTypedProjectionInputImage {
		t.Fatalf("response envelope typed projection = %q, want %q", result.TypedProjection, tools.ReadImageResultTypedProjectionInputImage)
	}
	part, ok := response.ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("response content part = %T, want messages.ImagePart", response.ContentParts[1])
	}
	if part.MediaType != wantMIME || !bytes.Equal(part.Bytes, wantBytes) {
		t.Fatalf("image part = (%q, %d bytes), want (%q, %d bytes)", part.MediaType, len(part.Bytes), wantMIME, len(wantBytes))
	}
}

func writeSessionReadImagePNG(t *testing.T, dir, name string, pixel color.RGBA) string {
	t.Helper()
	path := filepath.Join(dir, name)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, pixel)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadSessionReadImage(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeReadImageTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
