package services

import (
	"bytes"
	"context"
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
	if response.Content != "" || len(response.ContentParts) != 1 {
		t.Fatalf("response content = %q parts = %#v, want image-only rich result", response.Content, response.ContentParts)
	}
	part, ok := response.ContentParts[0].(messages.ImagePart)
	if !ok {
		t.Fatalf("response content part = %T, want messages.ImagePart", response.ContentParts[0])
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
