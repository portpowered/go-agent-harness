package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	readImagePositiveFixtureName = "read_image_positive.session.json"
	readImageMissingFixtureName  = "read_image_missing.session.json"
	readImageNegativeFixtureName = "read_image_negative.session.json"
	readImagePathPlaceholder     = "__READ_IMAGE_PATH__"
	readImageDataPlaceholder     = "__READ_IMAGE_DATA_URL__"
	readImageResultPlaceholder   = "__READ_IMAGE_RESULT__"
	readImageCallID              = "call_read_image_1"
)

func readImageToolImageItemID(callID string) string {
	digest := sha256.Sum256([]byte(callID))
	return "item_tool_result_" + base64.RawURLEncoding.EncodeToString(digest[:11])
}

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
	return materializeReadImageReplayFixtureMode(t, committedPath, imagePath, imageBytes, true)
}

// materializeReadImageReplayFixtureMode injects the test-owned path and exact
// image bytes into the hygiene-safe capture. When includeSessionClose is
// false, the final assistant MESSAGE.END is the only completion boundary, so
// the CLI test exercises the default lifecycle instead of a provider-close
// shortcut.
func materializeReadImageReplayFixtureMode(t *testing.T, committedPath, imagePath string, imageBytes []byte, includeSessionClose bool) string {
	t.Helper()
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)
	digest := sha256.Sum256(imageBytes)
	result, err := json.Marshal(tools.ReadImageResult{
		Version:         tools.ReadImageResultVersion,
		Status:          tools.ReadImageResultStatusSuccess,
		MIMEType:        "image/png",
		ByteLength:      len(imageBytes),
		SHA256:          hex.EncodeToString(digest[:]),
		TypedProjection: tools.ReadImageResultTypedProjectionInputImage,
	})
	if err != nil {
		t.Fatalf("marshal read_image result envelope: %v", err)
	}
	return materializeReadImageReplayResultFixture(t, committedPath, imagePath, dataURL, string(result), includeSessionClose)
}

func materializeReadImageMissingReplayFixtureMode(t *testing.T, committedPath, imagePath string, includeSessionClose bool) string {
	t.Helper()
	result, err := json.Marshal(tools.ReadImageResult{
		Version: tools.ReadImageResultVersion,
		Status:  tools.ReadImageResultStatusError,
		Error:   expectedReadImageMissingError(t, imagePath),
	})
	if err != nil {
		t.Fatalf("marshal missing read_image result envelope: %v", err)
	}
	return materializeReadImageReplayResultFixture(t, committedPath, imagePath, "", string(result), includeSessionClose)
}

func materializeReadImageReplayResultFixture(t *testing.T, committedPath, imagePath, dataURL, result string, includeSessionClose bool) string {
	t.Helper()
	capture := captureCopy(t, committedPath)
	for index := range capture.Records {
		record := &capture.Records[index]
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		record.Payload = rewriteReadImagePayload(t, payload, imagePath, dataURL, string(result))
		record.Data = nil
	}
	if !includeSessionClose {
		filtered := capture.Records[:0]
		for _, record := range capture.Records {
			if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
				continue
			}
			filtered = append(filtered, record)
		}
		capture.Records = filtered
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

func expectedReadImageMissingError(t *testing.T, imagePath string) string {
	t.Helper()
	_, err := os.ReadFile(imagePath)
	if err == nil {
		t.Fatalf("missing read_image path unexpectedly exists: %s", imagePath)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read_image path returned unexpected error: %v", err)
	}
	return fmt.Sprintf("session image %q is missing: %v", imagePath, err)
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
		"--workdir", filepath.Dir(imagePath),
		"session",
		"--replay", fixturePath,
		"--provider", "openai",
		"--model", "gpt-realtime",
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

// assertReadImageWireContract validates the provider-facing expectation that
// the strict replay will enforce. The replay transport compares every actual
// client frame before releasing the next server frame, so a mismatch here is
// not merely a fixture check: an empty, wrong, duplicate, or uncorrelated
// result prevents the continuation from being delivered to the CLI.
func assertReadImageWireContract(t *testing.T, fixturePath, imagePath string, expectedBytes []byte) {
	t.Helper()
	capture := captureCopy(t, fixturePath)
	wantDigest := sha256.Sum256(expectedBytes)
	wantDigestHex := hex.EncodeToString(wantDigest[:])
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(expectedBytes)

	callCount := 0
	callID := ""
	functionOutputCount := 0
	functionOutputIndex := -1
	imageItemCount := 0
	imageItemIndex := -1
	toolArgumentCount := 0
	encodedImageOccurrences := 0
	continuationResponseCreates := make([]int, 0, 2)
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_item.added" {
			var event struct {
				Item struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode read_image tool call: %v", err)
			}
			if event.Item.Type == "function_call" && event.Item.Name == tools.ReadImageToolID {
				callCount++
				callID = event.Item.CallID
			}
		}
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" {
			var event struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode read_image arguments: %v", err)
			}
			if event.CallID != readImageCallID || event.Name != tools.ReadImageToolID {
				t.Fatalf("read_image arguments correlation = (%q, %q), want (%q, %q)", event.CallID, event.Name, readImageCallID, tools.ReadImageToolID)
			}
			var arguments struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(event.Arguments), &arguments); err != nil {
				t.Fatalf("decode read_image arguments JSON: %v", err)
			}
			if arguments.Path != imagePath {
				t.Fatalf("wire read_image path = %q, want %q", arguments.Path, imagePath)
			}
			toolArgumentCount++
		}
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		encodedImageOccurrences += strings.Count(string(payload), base64.StdEncoding.EncodeToString(expectedBytes))
		switch record.Type {
		case "conversation.item.create":
			var event struct {
				Item struct {
					Type    string `json:"type"`
					CallID  string `json:"call_id"`
					Output  string `json:"output"`
					Role    string `json:"role"`
					ID      string `json:"id"`
					Content []struct {
						Type     string `json:"type"`
						ImageURL string `json:"image_url"`
					} `json:"content"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode read_image conversation item: %v", err)
			}
			switch event.Item.Type {
			case "function_call_output":
				functionOutputCount++
				functionOutputIndex = index
				if event.Item.CallID != readImageCallID {
					t.Fatalf("function_call_output call_id = %q, want %q", event.Item.CallID, readImageCallID)
				}
				if strings.TrimSpace(event.Item.Output) == "" {
					t.Fatal("function_call_output output is empty")
				}
				var result tools.ReadImageResult
				if err := json.Unmarshal([]byte(event.Item.Output), &result); err != nil {
					t.Fatalf("decode function_call_output read_image envelope: %v", err)
				}
				if result.Version != tools.ReadImageResultVersion || result.Status != tools.ReadImageResultStatusSuccess {
					t.Fatalf("function_call_output result = %#v, want versioned success", result)
				}
				if len(event.Item.Output) > 1024 || strings.Contains(strings.ToLower(event.Item.Output), "data:") || strings.Contains(strings.ToLower(event.Item.Output), "base64") {
					t.Fatalf("function_call_output is not a bounded metadata envelope: bytes=%d output=%q", len(event.Item.Output), event.Item.Output)
				}
				if result.MIMEType != "image/png" || result.ByteLength != len(expectedBytes) || result.SHA256 != wantDigestHex || result.TypedProjection != tools.ReadImageResultTypedProjectionInputImage {
					t.Fatalf("function_call_output result metadata = %#v, want MIME image/png, length %d, digest %s, typed image projection", result, len(expectedBytes), wantDigestHex)
				}
			case "message":
				if event.Item.ID != readImageToolImageItemID(readImageCallID) {
					continue
				}
				imageItemCount++
				imageItemIndex = index
				if event.Item.Role != string(messages.RoleUser) || len(event.Item.Content) != 1 || event.Item.Content[0].Type != "input_image" || event.Item.Content[0].ImageURL != wantDataURL {
					t.Fatalf("correlated input_image item = %#v, want one user image with exact fixture bytes", event.Item)
				}
			}
		case "response.create":
			continuationResponseCreates = append(continuationResponseCreates, index)
		}
	}

	if callCount != 1 || callID != readImageCallID {
		t.Fatalf("read_image calls = %d with call ID %q, want one call ID %q", callCount, callID, readImageCallID)
	}
	if toolArgumentCount != 1 {
		t.Fatalf("read_image argument event count = %d, want exactly one", toolArgumentCount)
	}
	if functionOutputCount != 1 {
		t.Fatalf("function_call_output count = %d, want exactly one", functionOutputCount)
	}
	if imageItemCount != 1 {
		t.Fatalf("correlated input_image count = %d, want exactly one", imageItemCount)
	}
	if encodedImageOccurrences != 1 {
		t.Fatalf("encoded image payload occurs %d times across client provider frames, want exactly once", encodedImageOccurrences)
	}
	if len(continuationResponseCreates) != 2 {
		t.Fatalf("response.create count = %d, want initial request plus exactly one continuation", len(continuationResponseCreates))
	}
	if !(functionOutputIndex < imageItemIndex && imageItemIndex < continuationResponseCreates[1]) {
		t.Fatalf("read_image transaction order = function output %d, image item %d, continuation %d; want output < image < continuation", functionOutputIndex, imageItemIndex, continuationResponseCreates[1])
	}
}

func assertReadImageMissingWireContract(t *testing.T, fixturePath, imagePath string) {
	t.Helper()
	capture := captureCopy(t, fixturePath)
	wantError := expectedReadImageMissingError(t, imagePath)

	callCount := 0
	callID := ""
	toolArgumentCount := 0
	functionOutputCount := 0
	functionOutputIndex := -1
	imageItemCount := 0
	continuationResponseCreates := make([]int, 0, 2)
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.output_item.added" {
			var event struct {
				Item struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode missing read_image tool call: %v", err)
			}
			if event.Item.Type == "function_call" && event.Item.Name == tools.ReadImageToolID {
				callCount++
				callID = event.Item.CallID
			}
		}
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" {
			var event struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode missing read_image arguments: %v", err)
			}
			if event.CallID != readImageCallID || event.Name != tools.ReadImageToolID {
				t.Fatalf("missing read_image arguments correlation = (%q, %q), want (%q, %q)", event.CallID, event.Name, readImageCallID, tools.ReadImageToolID)
			}
			var arguments struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(event.Arguments), &arguments); err != nil {
				t.Fatalf("decode missing read_image arguments JSON: %v", err)
			}
			if arguments.Path != imagePath {
				t.Fatalf("missing read_image path = %q, want %q", arguments.Path, imagePath)
			}
			toolArgumentCount++
		}
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		switch record.Type {
		case "conversation.item.create":
			var event struct {
				Item struct {
					Type    string `json:"type"`
					CallID  string `json:"call_id"`
					Output  string `json:"output"`
					Content []struct {
						Type string `json:"type"`
					} `json:"content"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode missing read_image conversation item: %v", err)
			}
			switch event.Item.Type {
			case "function_call_output":
				functionOutputCount++
				functionOutputIndex = index
				if event.Item.CallID != readImageCallID {
					t.Fatalf("missing function_call_output call_id = %q, want %q", event.Item.CallID, readImageCallID)
				}
				if strings.TrimSpace(event.Item.Output) == "" {
					t.Fatal("missing read_image function_call_output is empty")
				}
				var result tools.ReadImageResult
				if err := json.Unmarshal([]byte(event.Item.Output), &result); err != nil {
					t.Fatalf("decode missing read_image error envelope: %v", err)
				}
				if result.Version != tools.ReadImageResultVersion || result.Status != tools.ReadImageResultStatusError || result.Error != wantError {
					t.Fatalf("missing read_image result = %#v, want version %d error %q", result, tools.ReadImageResultVersion, wantError)
				}
				if result.MIMEType != "" || result.ByteLength != 0 || result.SHA256 != "" || result.TypedProjection != "" {
					t.Fatalf("missing read_image result unexpectedly carried image metadata: %#v", result)
				}
			case "message":
				for _, part := range event.Item.Content {
					if part.Type == "input_image" {
						imageItemCount++
					}
				}
			}
		case "response.create":
			continuationResponseCreates = append(continuationResponseCreates, index)
		}
	}

	if callCount != 1 || callID != readImageCallID {
		t.Fatalf("missing read_image calls = %d with call ID %q, want one call ID %q", callCount, callID, readImageCallID)
	}
	if toolArgumentCount != 1 {
		t.Fatalf("missing read_image argument event count = %d, want exactly one", toolArgumentCount)
	}
	if functionOutputCount != 1 {
		t.Fatalf("missing function_call_output count = %d, want exactly one", functionOutputCount)
	}
	if imageItemCount != 0 {
		t.Fatalf("missing read_image emitted %d input_image item(s), want none", imageItemCount)
	}
	if len(continuationResponseCreates) != 2 {
		t.Fatalf("missing read_image response.create count = %d, want initial request plus exactly one continuation", len(continuationResponseCreates))
	}
	if functionOutputIndex >= continuationResponseCreates[1] {
		t.Fatalf("missing read_image transaction order = function output %d, continuation %d; want output before continuation", functionOutputIndex, continuationResponseCreates[1])
	}
}

// rewriteReadImageCapture copies a materialized capture and applies a wire
// mutation. It is used only for negative controls that must fail at the
// provider boundary, before scripted grounded prose can be delivered.
func rewriteReadImageCapture(t *testing.T, source string, mutate func(map[string]any)) string {
	t.Helper()
	capture := captureCopy(t, source)
	mutated := false
	for index := range capture.Records {
		record := &capture.Records[index]
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != "conversation.item.create" {
			continue
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		var decoded map[string]any
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("decode read_image mutation payload: %v", err)
		}
		item, ok := decoded["item"].(map[string]any)
		if !ok || item["type"] != "function_call_output" {
			continue
		}
		mutate(item)
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("encode read_image mutation payload: %v", err)
		}
		record.Payload = encoded
		record.Data = nil
		mutated = true
	}
	if !mutated {
		t.Fatal("read_image mutation found no function_call_output")
	}
	path := filepath.Join(t.TempDir(), "read_image_mutated.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal mutated read_image capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write mutated read_image capture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("mutated read_image capture rejected: %v", err)
	}
	return path
}

func assertReadImageGrounded(output string, events []messages.StreamMessage, imagePath string, expectedBytes []byte) error {
	return assertReadImageGroundedWithProviderClose(output, events, imagePath, expectedBytes, true)
}

func assertReadImageGroundedWithProviderClose(output string, events []messages.StreamMessage, imagePath string, expectedBytes []byte, requireProviderClose bool) error {
	if requireProviderClose && !strings.Contains(output, "[session closed: fixture_complete]") {
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

// TestReadImageCLI_DefaultLifecycleWaitsForStrictContinuation drives the
// model-initiated read_image transaction through the shipped CLI composition.
// The provider-close record is intentionally omitted: the default session
// path must finish on the final assistant response, after the strict replay
// has accepted the non-empty function result, correlated image projection,
// and exactly one continuation request.
func TestReadImageCLI_DefaultLifecycleWaitsForStrictContinuation(t *testing.T) {
	imagePath := readImageFixturePath(t)
	imageBytes := readImageFixtureBytes(t)
	if err := assertReadImageFixturePixels(imageBytes); err != nil {
		t.Fatal(err)
	}

	configDir := writeReadImageConfig(t, true)
	fixture := materializeReadImageReplayFixtureMode(t, readImageReplayFixturePath(t, readImagePositiveFixtureName), imagePath, imageBytes, false)
	assertReadImageToolAdvertisement(t, fixture, true)
	assertReadImageWireContract(t, fixture, imagePath, imageBytes)

	observer := &readImageSessionObserver{}
	output, runErr := runReadImageSession(t, fixture, configDir, imagePath, observer)
	if runErr != nil {
		t.Fatalf("default read_image CLI replay failed: %v\noutput: %s", runErr, output)
	}
	if err := assertReadImageGroundedWithProviderClose(output, observer.snapshot(), imagePath, imageBytes, false); err != nil {
		t.Fatal(err)
	}

	events := observer.snapshot()
	imageEnd := -1
	finalAssistantEnd := -1
	for index, event := range events {
		if event.Role == messages.RoleTool && event.Type == messages.StreamTypeImageEnd {
			imageEnd = index
		}
		if event.Type == messages.StreamTypeMessageEnd && event.Role != messages.RoleTool && imageEnd >= 0 {
			finalAssistantEnd = index
		}
	}
	if imageEnd < 0 || finalAssistantEnd <= imageEnd {
		t.Fatalf("default lifecycle ended without a post-image assistant continuation: image_end=%d final_assistant_end=%d events=%#v", imageEnd, finalAssistantEnd, events)
	}
}

// TestReadImageCLI_DefaultLifecycleRejectsEmptyFunctionOutput is a negative
// control for the strict provider boundary. The same grounded server reply is
// present, but replay must reject an empty function_call_output before it can
// release that reply to the CLI.
func TestReadImageCLI_DefaultLifecycleRejectsEmptyFunctionOutput(t *testing.T) {
	imagePath := readImageFixturePath(t)
	imageBytes := readImageFixtureBytes(t)
	configDir := writeReadImageConfig(t, true)
	validFixture := materializeReadImageReplayFixtureMode(t, readImageReplayFixturePath(t, readImagePositiveFixtureName), imagePath, imageBytes, false)
	fixture := rewriteReadImageCapture(t, validFixture, func(item map[string]any) {
		item["output"] = ""
	})

	output, runErr := runReadImageSession(t, fixture, configDir, imagePath, &readImageSessionObserver{})
	if runErr == nil {
		t.Fatalf("empty function_call_output completed cleanly; output=%s", output)
	}
	if !errors.Is(runErr, providers.ErrReplayMismatch) {
		t.Fatalf("empty function_call_output error = %v, want typed replay mismatch", runErr)
	}
	for _, marker := range readImageGroundedMarkers {
		if strings.Contains(output, marker) {
			t.Fatalf("empty function_call_output released fabricated grounded reply %q: %s", marker, output)
		}
	}
}

// TestReadImageCLI_DefaultLifecycleMissingFileContinues proves the failed
// read_image transaction through the shipped CLI composition. The capture has
// no provider-close record, so the default lifecycle must consume the one
// continuation after the non-empty error result before returning.
func TestReadImageCLI_DefaultLifecycleMissingFileContinues(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "guaranteed-missing-read-image.png")
	configDir := writeReadImageConfig(t, true)
	committedFixture := readImageReplayFixturePath(t, readImageMissingFixtureName)
	if violations := gwtesting.ValidateSessionCaptureFile(committedFixture); len(violations) != 0 {
		t.Fatalf("committed missing read_image fixture failed validation: %v", violations)
	}
	fixture := materializeReadImageMissingReplayFixtureMode(t, committedFixture, missingPath, false)
	assertReadImageToolAdvertisement(t, fixture, true)
	assertReadImageMissingWireContract(t, fixture, missingPath)

	observer := &readImageSessionObserver{}
	output, runErr := runReadImageSession(t, fixture, configDir, missingPath, observer)
	if runErr != nil {
		t.Fatalf("default missing read_image CLI replay failed: %v\noutput: %s", runErr, output)
	}
	if strings.Contains(output, "use of closed network connection") {
		t.Fatalf("missing read_image session reported a closed network error: %s", output)
	}
	if !strings.Contains(strings.ToLower(output), "could not read the image") || !strings.Contains(strings.ToLower(output), "missing") {
		t.Fatalf("missing read_image response did not explain the missing file: %s", output)
	}
	for _, marker := range readImageGroundedMarkers {
		if strings.Contains(output, marker) {
			t.Fatalf("missing read_image response fabricated grounded marker %q: %s", marker, output)
		}
	}

	events := observer.snapshot()
	toolCallIndex := -1
	assistantMessageStarts := 0
	continuationMessageStart := -1
	finalAssistantEnd := -1
	for index, event := range events {
		if value, ok := event.Value.(*messages.ToolCallEndValue); ok && value != nil && value.Name == tools.ReadImageToolID {
			if toolCallIndex >= 0 {
				t.Fatalf("missing read_image observed duplicate tool call: %#v", events)
			}
			toolCallIndex = index
		}
		if event.Type == messages.StreamTypeImageStart || event.Type == messages.StreamTypeImageDelta || event.Type == messages.StreamTypeImageEnd {
			if event.Role == messages.RoleTool || event.ToolCallId == readImageCallID {
				t.Fatalf("missing read_image emitted image result event: %#v", event)
			}
		}
		if event.Type == messages.StreamTypeMessageStart && event.Role != messages.RoleTool {
			assistantMessageStarts++
			if assistantMessageStarts == 2 {
				continuationMessageStart = index
			}
		}
		if event.Type == messages.StreamTypeMessageEnd && event.Role != messages.RoleTool && continuationMessageStart >= 0 {
			finalAssistantEnd = index
		}
	}
	if toolCallIndex < 0 || continuationMessageStart <= toolCallIndex || finalAssistantEnd <= continuationMessageStart {
		t.Fatalf("missing read_image session did not reach a terminal assistant continuation: tool_call=%d continuation_start=%d assistant_starts=%d assistant_end=%d events=%#v", toolCallIndex, continuationMessageStart, assistantMessageStarts, finalAssistantEnd, events)
	}
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
