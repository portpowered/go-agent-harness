package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	readImagePositiveFixtureName = "read_image_positive.session.json"
	readImageNegativeFixtureName = "read_image_negative.session.json"
	readImagePathPlaceholder     = "__READ_IMAGE_PATH__"
	readImageDataPlaceholder     = "__READ_IMAGE_DATA_URL__"
	readImageResultPlaceholder   = "__READ_IMAGE_RESULT__"
	readImageCallID              = "call_read_image_1"
)

var readImageGroundedMarkers = []string{
	"one-by-one image",
	"indigo pixel",
}

func readImageFixturePath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve read_image fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "testdata", "images", "fixture.png")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed read_image PNG fixture not found: %v", err)
	}
	return path
}

func readImageReplayFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve read_image replay fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "testdata", "s2s-e2e-read-image", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed read_image replay fixture %q not found: %v", name, err)
	}
	return path
}

func readImageFixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(readImageFixturePath(t))
	if err != nil {
		t.Fatalf("read committed read_image PNG fixture: %v", err)
	}
	return data
}

func assertReadImageFixturePixels(data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode fixture PNG: %w", err)
	}
	if got := img.Bounds().Size(); got.X != 1 || got.Y != 1 {
		return fmt.Errorf("fixture dimensions = %s, want 1x1", got)
	}
	pixel, ok := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA)
	if !ok {
		return fmt.Errorf("fixture pixel type = %T, want color.NRGBA", color.NRGBAModel.Convert(img.At(0, 0)))
	}
	if pixel != (color.NRGBA{R: 0x4f, G: 0x46, B: 0xe5, A: 0xff}) {
		return fmt.Errorf("fixture pixel = %#v, want indigo #4f46e5", pixel)
	}
	return nil
}

// readImageSessionObserver records the stream crossing the real CLI session
// loop. Image chunks are copied because the observer is deliberately an
// observational seam and must not retain mutable provider buffers.
type readImageSessionObserver struct {
	mu     sync.Mutex
	events []messages.StreamMessage
}

func (o *readImageSessionObserver) observe(msg messages.StreamMessage) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if imageDelta, ok := msg.Value.(*messages.ImageDeltaValue); ok && imageDelta != nil {
		copied := *imageDelta
		copied.Content = append([]byte(nil), imageDelta.Content...)
		msg.Value = &copied
	}
	o.events = append(o.events, msg)
}

func (o *readImageSessionObserver) snapshot() []messages.StreamMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]messages.StreamMessage, len(o.events))
	copy(out, o.events)
	return out
}

func materializeReadImageReplayFixture(t *testing.T, committedPath, imagePath string, imageBytes []byte) string {
	t.Helper()
	capture := captureCopy(t, committedPath)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	digest := sha256.Sum256(imageBytes)
	result, err := json.Marshal(tools.ReadImageResult{
		Version:    tools.ReadImageResultVersion,
		Status:     tools.ReadImageResultStatusSuccess,
		MIMEType:   "image/png",
		ByteLength: len(imageBytes),
		SHA256:     hex.EncodeToString(digest[:]),
		DataURL:    dataURL,
	})
	if err != nil {
		t.Fatalf("marshal read_image result envelope: %v", err)
	}
	for index := range capture.Records {
		record := &capture.Records[index]
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		record.Payload = rewriteReadImagePayload(t, payload, imagePath, dataURL, string(result))
		record.Data = nil
	}
	path := filepath.Join(t.TempDir(), filepath.Base(committedPath))
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal materialized read_image replay fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write materialized read_image replay fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("materialized read_image replay fixture rejected: %v", err)
	}
	return path
}

func rewriteReadImagePayload(t *testing.T, raw json.RawMessage, imagePath, dataURL, result string) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode read_image replay payload %s: %v", raw, err)
	}
	var rewrite func(any) any
	rewrite = func(current any) any {
		switch typed := current.(type) {
		case string:
			if typed == readImageResultPlaceholder {
				return result
			}
			return strings.ReplaceAll(strings.ReplaceAll(typed, readImagePathPlaceholder, imagePath), readImageDataPlaceholder, dataURL)
		case []any:
			for index := range typed {
				typed[index] = rewrite(typed[index])
			}
		case map[string]any:
			for key, child := range typed {
				typed[key] = rewrite(child)
			}
		}
		return current
	}
	encoded, err := json.Marshal(rewrite(value))
	if err != nil {
		t.Fatalf("encode read_image replay payload: %v", err)
	}
	return encoded
}

func writeReadImageConfig(t *testing.T, readImageEnabled bool) string {
	return writeReadImageModelConfig(t, readImageEnabled, "gpt-realtime")
}

func writeReadImageModelConfig(t *testing.T, readImageEnabled bool, model string) string {
	t.Helper()
	dir := t.TempDir()
	var configYAML strings.Builder
	fmt.Fprintf(&configYAML, "model:\n  provider: openai\n  openai:\n    model: %s\n", model)
	configYAML.WriteString("tools:\n  list:\n")
	for _, id := range config.DefaultToolIDs {
		enabled := readImageEnabled && id == "read_image"
		fmt.Fprintf(&configYAML, "    - id: %s\n      enabled: %t\n", id, enabled)
	}
	if err := os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(configYAML.String()), 0o600); err != nil {
		t.Fatalf("write read_image test config: %v", err)
	}
	return dir
}

func runReadImageSession(t *testing.T, fixturePath, configDir, imagePath string, observer *readImageSessionObserver) (string, error) {
	t.Helper()
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI composition: %v", err)
	}
	agentCLI.SetSessionStreamObserver(observer.observe)

	stdout := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(io.Discard)
	prompt := "Please inspect the image at " + imagePath + " without relying on its filename."
	rootCmd.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--replay", fixturePath,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--max-duration", "3s",
		prompt,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	return stdout.String(), err
}

func assertReadImageToolAdvertisement(t *testing.T, fixturePath string, wantReadImage bool) {
	t.Helper()
	capture := captureCopy(t, fixturePath)
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != "session.update" {
			continue
		}
		var payload struct {
			Session struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode session.update: %v", err)
		}
		if wantReadImage {
			if len(payload.Session.Tools) != 1 || payload.Session.Tools[0].Name != "read_image" {
				t.Fatalf("advertised tools = %#v, want only read_image", payload.Session.Tools)
			}
		} else if len(payload.Session.Tools) != 0 {
			t.Fatalf("advertised tools = %#v, want no tools", payload.Session.Tools)
		}
		return
	}
	t.Fatal("replay fixture has no client session.update event")
}

func assertReadImageGrounded(output string, events []messages.StreamMessage, imagePath string, expectedBytes []byte) error {
	if !strings.Contains(output, "[session closed: fixture_complete]") {
		return fmt.Errorf("session did not complete cleanly, got:\n%s", output)
	}
	for _, marker := range readImageGroundedMarkers {
		if !strings.Contains(output, marker) {
			return fmt.Errorf("response missing grounded visual fact %q, got:\n%s", marker, output)
		}
	}
	if err := assertReadImageFixturePixels(expectedBytes); err != nil {
		return err
	}

	var toolCalls []*messages.ToolCallEndValue
	providerMessageStarts := make([]int, 0, 2)
	imageCallID := ""
	imageMediaType := ""
	imageBytes := make([]byte, 0)
	imageStartIndex := -1
	imageEndIndex := -1
	for index, event := range events {
		switch value := event.Value.(type) {
		case *messages.ToolCallEndValue:
			if value != nil && value.Name == "read_image" {
				toolCalls = append(toolCalls, value)
			}
		case *messages.MessageStartValue:
			if event.Role != messages.RoleTool {
				providerMessageStarts = append(providerMessageStarts, index)
			}
		case *messages.ImageStartValue:
			if event.Role == messages.RoleTool {
				if imageStartIndex >= 0 {
					return fmt.Errorf("more than one tool image start observed")
				}
				imageStartIndex = index
				imageCallID = event.ToolCallId
				if value != nil {
					imageMediaType = value.MediaType
				}
			}
		case *messages.ImageDeltaValue:
			if event.Role == messages.RoleTool {
				if imageCallID == "" || event.ToolCallId != imageCallID {
					return fmt.Errorf("tool image delta call ID = %q, want %q", event.ToolCallId, imageCallID)
				}
				if value != nil {
					imageBytes = append(imageBytes, value.Content...)
				}
			}
		case *messages.ImageEndValue:
			if event.Role == messages.RoleTool {
				if imageEndIndex >= 0 || event.ToolCallId != imageCallID {
					return fmt.Errorf("tool image end is missing or has call ID %q, want %q", event.ToolCallId, imageCallID)
				}
				imageEndIndex = index
			}
		}
	}
	if len(toolCalls) != 1 {
		return fmt.Errorf("read_image tool calls = %d, want exactly one", len(toolCalls))
	}
	var arguments struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(toolCalls[0].Arguments), &arguments); err != nil {
		return fmt.Errorf("decode read_image arguments: %w", err)
	}
	if arguments.Path != imagePath {
		return fmt.Errorf("read_image path = %q, want committed fixture path %q", arguments.Path, imagePath)
	}
	if toolCalls[0].ToolCallID == "" || imageCallID != toolCalls[0].ToolCallID {
		return fmt.Errorf("tool image call ID = %q, want read_image call ID %q", imageCallID, toolCalls[0].ToolCallID)
	}
	if imageMediaType != "image/png" {
		return fmt.Errorf("tool image MIME = %q, want image/png", imageMediaType)
	}
	if imageStartIndex < 0 || imageEndIndex <= imageStartIndex || !bytes.Equal(imageBytes, expectedBytes) {
		return fmt.Errorf("tool image bytes were not preserved exactly (start=%d end=%d bytes=%d want=%d)", imageStartIndex, imageEndIndex, len(imageBytes), len(expectedBytes))
	}
	if len(providerMessageStarts) < 2 {
		return fmt.Errorf("provider message starts = %d, want initial response plus post-image response", len(providerMessageStarts))
	}
	if imageEndIndex >= providerMessageStarts[1] {
		return fmt.Errorf("post-tool provider response started at event %d before image ended at event %d", providerMessageStarts[1], imageEndIndex)
	}
	return nil
}

func assertReadImageNoToolGrounding(output string, events []messages.StreamMessage, expectedBytes []byte) error {
	if !strings.Contains(output, "[session closed: fixture_complete]") {
		return fmt.Errorf("no-tool session did not complete cleanly, got:\n%s", output)
	}
	if !strings.Contains(output, "cannot inspect") || !strings.Contains(output, "determine") {
		return fmt.Errorf("no-tool response did not state that image content is unavailable, got:\n%s", output)
	}
	for _, marker := range readImageGroundedMarkers {
		if strings.Contains(output, marker) {
			return fmt.Errorf("no-tool response leaked grounded marker %q: %s", marker, output)
		}
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(expectedBytes)
	if strings.Contains(output, dataURL) {
		return fmt.Errorf("no-tool response leaked image data URL")
	}
	for _, event := range events {
		switch value := event.Value.(type) {
		case *messages.ToolCallEndValue:
			if value != nil && value.Name == "read_image" {
				return fmt.Errorf("no-tool session executed read_image")
			}
		case *messages.ImageStartValue, *messages.ImageDeltaValue, *messages.ImageEndValue:
			return fmt.Errorf("no-tool session emitted image result event %s", event.Type)
		}
	}
	return nil
}

func TestReadImageCLI_GroundedPositiveAndNoToolNegative(t *testing.T) {
	imagePath := readImageFixturePath(t)
	imageBytes := readImageFixtureBytes(t)
	if err := assertReadImageFixturePixels(imageBytes); err != nil {
		t.Fatal(err)
	}

	positiveCommitted := readImageReplayFixturePath(t, readImagePositiveFixtureName)
	negativeCommitted := readImageReplayFixturePath(t, readImageNegativeFixtureName)
	for _, path := range []string{positiveCommitted, negativeCommitted} {
		if errs := gwtesting.ValidateSessionCaptureFile(path); len(errs) != 0 {
			t.Fatalf("committed fixture %s violates capture hygiene: %v", path, errs)
		}
	}

	t.Run("tool enabled grounds response", func(t *testing.T) {
		configDir := writeReadImageConfig(t, true)
		fixture := materializeReadImageReplayFixture(t, positiveCommitted, imagePath, imageBytes)
		observer := &readImageSessionObserver{}
		output, runErr := runReadImageSession(t, fixture, configDir, imagePath, observer)
		if runErr != nil {
			t.Fatalf("read_image CLI replay: %v\noutput: %s", runErr, output)
		}
		assertReadImageToolAdvertisement(t, fixture, true)
		if err := assertReadImageGrounded(output, observer.snapshot(), imagePath, imageBytes); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tool disabled cannot inspect", func(t *testing.T) {
		configDir := writeReadImageConfig(t, false)
		fixture := materializeReadImageReplayFixture(t, negativeCommitted, imagePath, imageBytes)
		observer := &readImageSessionObserver{}
		output, runErr := runReadImageSession(t, fixture, configDir, imagePath, observer)
		if runErr != nil {
			t.Fatalf("no-tool CLI replay: %v\noutput: %s", runErr, output)
		}
		assertReadImageToolAdvertisement(t, fixture, false)
		if err := assertReadImageNoToolGrounding(output, observer.snapshot(), imageBytes); err != nil {
			t.Fatal(err)
		}
	})
}

func TestReadImageGroundingCheckerRejectsMissingOrReplacedPixels(t *testing.T) {
	imagePath := readImageFixturePath(t)
	imageBytes := readImageFixtureBytes(t)
	arguments, err := json.Marshal(map[string]string{"path": imagePath})
	if err != nil {
		t.Fatal(err)
	}
	positiveEvents := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallEnd, ToolCallId: readImageCallID, Value: messages.NewToolCallEndValue(readImageCallID, "read_image", string(arguments))},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleTool, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeImageStart, Role: messages.RoleTool, ToolCallId: readImageCallID, Value: messages.NewImageStartValue("image/png")},
		{Type: messages.StreamTypeImageDelta, Role: messages.RoleTool, ToolCallId: readImageCallID, Value: messages.NewImageDeltaValue(imageBytes)},
		{Type: messages.StreamTypeImageEnd, Role: messages.RoleTool, ToolCallId: readImageCallID, Value: messages.NewImageEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
	}
	output := "The image is a one-by-one image with a single indigo pixel.\n[session closed: fixture_complete]\n"
	if err := assertReadImageGrounded(output, positiveEvents, imagePath, imageBytes); err != nil {
		t.Fatalf("known-good grounding evidence rejected: %v", err)
	}

	missing := cloneReadImageEvents(positiveEvents)
	missing[4].Value = messages.NewImageDeltaValue(nil)
	if err := assertReadImageGrounded(output, missing, imagePath, imageBytes); err == nil {
		t.Fatal("grounding checker accepted a missing image payload")
	}

	replaced := cloneReadImageEvents(positiveEvents)
	replaced[4].Value = messages.NewImageDeltaValue([]byte("different pixels"))
	if err := assertReadImageGrounded(output, replaced, imagePath, imageBytes); err == nil {
		t.Fatal("grounding checker accepted a replaced image payload")
	}
}

func cloneReadImageEvents(events []messages.StreamMessage) []messages.StreamMessage {
	out := make([]messages.StreamMessage, len(events))
	copy(out, events)
	for index, event := range out {
		if imageDelta, ok := event.Value.(*messages.ImageDeltaValue); ok && imageDelta != nil {
			copied := *imageDelta
			copied.Content = append([]byte(nil), imageDelta.Content...)
			out[index].Value = &copied
		}
	}
	return out
}
