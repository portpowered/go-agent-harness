//go:build live

// Opt-in live proof that the shipped session CLI can use read_image with a
// vision-capable OpenAI Realtime model. The hermetic test remains the default
// evidence; this variant is bounded and requires an explicit billing opt-in.
package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveReadImageModel   = "gpt-realtime-2.1-mini"
	liveReadImageTimeout = 30 * time.Second
)

func TestLiveReadImageCLI_GroundedPositiveAndNoToolNegative(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live OpenAI Realtime read_image round trip")
	}
	if os.Getenv("AGENT_HARNESS_LIVE_READ_IMAGE") != "1" {
		t.Skip("AGENT_HARNESS_LIVE_READ_IMAGE!=1; this live test bills real API usage and must be opted into explicitly")
	}

	imagePath := readImageFixturePath(t)
	imageBytes := readImageFixtureBytes(t)
	if err := assertReadImageFixturePixels(imageBytes); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name             string
		readImageEnabled bool
	}{
		{name: "tool enabled grounds response", readImageEnabled: true},
		{name: "tool disabled cannot inspect", readImageEnabled: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := writeReadImageModelConfig(t, testCase.readImageEnabled, liveReadImageModel)
			output, events, capturePath, runErr := runLiveReadImageSession(t, apiKey, configDir, imagePath)
			if runErr != nil && !strings.Contains(runErr.Error(), "context deadline exceeded") {
				t.Fatalf("live read_image session: %v\noutput: %s", runErr, output)
			}
			assertReadImageToolAdvertisement(t, capturePath, testCase.readImageEnabled)
			if testCase.readImageEnabled {
				assertLiveReadImageGrounded(t, output, events, imagePath, imageBytes)
				return
			}
			assertLiveReadImageNoToolGrounding(t, output, events, imageBytes)
		})
	}
}

func runLiveReadImageSession(t *testing.T, apiKey, configDir, imagePath string) (string, []messages.StreamMessage, string, error) {
	t.Helper()
	workDir := t.TempDir()
	capturePath := filepath.Join(workDir, "read-image-live.session.json")
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI composition: %v", err)
	}
	observer := &readImageSessionObserver{}
	agentCLI.SetSessionStreamObserver(observer.observe)

	stdout := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--provider", "openai",
		"--model", liveReadImageModel,
		"--api-key", apiKey,
		"--record", capturePath,
		"--max-duration", liveReadImageTimeout.String(),
		"Please inspect the image at " + imagePath + " and report its exact dimensions and the dominant pixel color without relying on its filename.",
	})
	ctx, cancel := context.WithTimeout(context.Background(), liveReadImageTimeout+5*time.Second)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	if _, err := gwtesting.LoadSessionCapture(capturePath); err != nil {
		t.Fatalf("load live read_image capture (run error: %v): %v", runErr, err)
	}
	return stdout.String(), observer.snapshot(), capturePath, runErr
}

func assertLiveReadImageGrounded(t *testing.T, output string, events []messages.StreamMessage, imagePath string, expectedBytes []byte) {
	t.Helper()
	toolCallIndex := -1
	toolCallID := ""
	var toolCallArguments string
	imageStartIndex := -1
	imageEndIndex := -1
	imageMediaType := ""
	imageBytes := make([]byte, 0, len(expectedBytes))
	providerMessageStarts := make([]int, 0, 2)
	for index, event := range events {
		switch value := event.Value.(type) {
		case *messages.ToolCallEndValue:
			if value != nil && value.Name == "read_image" {
				if toolCallIndex >= 0 {
					t.Fatalf("live session executed read_image more than once")
				}
				toolCallIndex = index
				toolCallID = value.ToolCallID
				toolCallArguments = value.Arguments
			}
		case *messages.MessageStartValue:
			if event.Role != messages.RoleTool {
				providerMessageStarts = append(providerMessageStarts, index)
			}
		case *messages.ImageStartValue:
			if event.Role == messages.RoleTool {
				if imageStartIndex >= 0 {
					t.Fatalf("live session emitted more than one tool image")
				}
				imageStartIndex = index
				if toolCallID != "" && event.ToolCallId != toolCallID {
					t.Fatalf("live image start call ID = %q, want %q", event.ToolCallId, toolCallID)
				}
				toolCallID = firstNonEmptyReadImageID(toolCallID, event.ToolCallId)
				if value != nil {
					imageMediaType = value.MediaType
				}
			}
		case *messages.ImageDeltaValue:
			if event.Role == messages.RoleTool {
				if event.ToolCallId != toolCallID {
					t.Fatalf("live image delta call ID = %q, want %q", event.ToolCallId, toolCallID)
				}
				if value != nil {
					imageBytes = append(imageBytes, value.Content...)
				}
			}
		case *messages.ImageEndValue:
			if event.Role == messages.RoleTool {
				if imageEndIndex >= 0 || event.ToolCallId != toolCallID {
					t.Fatalf("live image end call ID = %q, want %q", event.ToolCallId, toolCallID)
				}
				imageEndIndex = index
			}
		}
	}
	if toolCallIndex < 0 {
		t.Fatalf("live response never called read_image; output: %s", output)
	}
	var arguments struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(toolCallArguments), &arguments); err != nil {
		t.Fatalf("decode live read_image arguments: %v", err)
	}
	if arguments.Path != imagePath {
		t.Fatalf("live read_image path = %q, want %q", arguments.Path, imagePath)
	}
	if imageMediaType != "image/png" || imageStartIndex < 0 || imageEndIndex <= imageStartIndex || !bytes.Equal(imageBytes, expectedBytes) {
		t.Fatalf("live image evidence was not preserved (MIME=%q start=%d end=%d bytes=%d want=%d)", imageMediaType, imageStartIndex, imageEndIndex, len(imageBytes), len(expectedBytes))
	}
	if len(providerMessageStarts) < 2 || imageEndIndex >= providerMessageStarts[1] {
		t.Fatalf("live provider did not start a response after the complete image result (starts=%v image_end=%d)", providerMessageStarts, imageEndIndex)
	}

	text := strings.ToLower(output + "\n" + readImageAssistantText(events))
	if !strings.Contains(text, "indigo") || !strings.Contains(text, "pixel") || (!strings.Contains(text, "one") && !strings.Contains(text, "1")) {
		t.Fatalf("live response did not name the fixture's grounded visual facts: %s", output)
	}
}

func assertLiveReadImageNoToolGrounding(t *testing.T, output string, events []messages.StreamMessage, expectedBytes []byte) {
	t.Helper()
	for _, event := range events {
		switch value := event.Value.(type) {
		case *messages.ToolCallEndValue:
			if value != nil && value.Name == "read_image" {
				t.Fatalf("live no-tool session executed read_image")
			}
		case *messages.ImageStartValue, *messages.ImageDeltaValue, *messages.ImageEndValue:
			if event.Role == messages.RoleTool {
				t.Fatalf("live no-tool session emitted image event %s", event.Type)
			}
		}
	}
	text := strings.ToLower(output + "\n" + readImageAssistantText(events))
	if strings.Contains(text, "indigo") {
		t.Fatalf("live no-tool response named grounded visual facts: %s", output)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(expectedBytes)
	if strings.Contains(text, dataURL) {
		t.Fatalf("live no-tool response leaked image data")
	}
	if !((strings.Contains(text, "cannot") || strings.Contains(text, "can't") || strings.Contains(text, "unable")) &&
		(strings.Contains(text, "inspect") || strings.Contains(text, "access") || strings.Contains(text, "determine"))) {
		t.Fatalf("live no-tool response did not explain that image evidence is unavailable: %s", output)
	}
}

func readImageAssistantText(events []messages.StreamMessage) string {
	var text strings.Builder
	for _, event := range events {
		if event.Role == messages.RoleTool {
			continue
		}
		switch event.Type {
		case messages.StreamTypeTextDelta:
			if value, ok := event.Value.(*messages.TextDeltaValue); ok && value != nil {
				text.WriteString(value.Content)
			}
		case messages.StreamTypeTranscriptDelta:
			if value, ok := event.Value.(*messages.TranscriptDeltaValue); ok && value != nil {
				text.WriteString(value.Text)
			}
		}
	}
	return text.String()
}

func firstNonEmptyReadImageID(first, second string) string {
	if first != "" {
		return first
	}
	return second
}
