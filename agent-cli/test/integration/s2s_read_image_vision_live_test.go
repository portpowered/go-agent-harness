//go:build live

// Opt-in live proof that the shipped session CLI can use read_image with a
// vision-capable OpenAI Realtime model. The hermetic tests remain the required
// default evidence; this variant is bounded and requires an explicit billing
// opt-in.
package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveReadImageModel   = "gpt-realtime-2.1-mini"
	liveReadImageTimeout = 30 * time.Second
)

// TestLiveReadImageCLI_DefaultReadableAndMissing proves the two customer
// paths against the real OpenAI Realtime WebSocket. It deliberately omits
// --wait-for-close: a passing readable or missing path must return only after
// the post-tool continuation has produced its terminal response.
func TestLiveReadImageCLI_DefaultReadableAndMissing(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live OpenAI Realtime read_image proof")
	}
	if os.Getenv("AGENT_HARNESS_LIVE_READ_IMAGE") != "1" {
		t.Skip("AGENT_HARNESS_LIVE_READ_IMAGE!=1; this live test bills real API usage and must be opted into explicitly")
	}

	imagePath := readImageFixturePath(t)
	imageBytes := readImageFixtureBytes(t)
	if err := assertReadImageFixturePixels(imageBytes); err != nil {
		t.Fatal(err)
	}

	missingPath := filepath.Join(t.TempDir(), "guaranteed-missing-read-image.png")
	for _, testCase := range []struct {
		name       string
		imagePath  string
		expected   []byte
		wantImage  bool
		promptTail string
	}{
		{
			name:       "readable image reaches grounded continuation",
			imagePath:  imagePath,
			expected:   imageBytes,
			wantImage:  true,
			promptTail: "report the exact dimensions and the dominant pixel color after the tool returns",
		},
		{
			name:       "missing image reaches failure continuation",
			imagePath:  missingPath,
			wantImage:  false,
			promptTail: "explain accurately why the image could not be read after the tool returns",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := writeReadImageModelConfig(t, true, liveReadImageModel)
			prompt := fmt.Sprintf("Use the read_image tool exactly once on the local image at %s; do not infer the result from the path or from this prompt, and %s.", testCase.imagePath, testCase.promptTail)
			run := runLiveReadImageSession(t, os.Getenv("OPENAI_API_KEY"), configDir, prompt)
			if run.err != nil {
				t.Fatalf("live read_image session did not complete cleanly: %v", run.err)
			}
			combinedOutput := strings.ToLower(run.output + "\n" + run.stderr)
			if strings.Contains(combinedOutput, "use of closed network connection") || strings.Contains(combinedOutput, "closed network connection") {
				t.Fatalf("live read_image session reported a closed-network error: %s", run.stderr)
			}

			observation := assertLiveReadImageWireContract(t, run.capture, testCase.imagePath, testCase.expected, testCase.wantImage)
			finalText := observation.finalText
			assertLiveReadImageSemanticResult(t, run.output, finalText, testCase.wantImage)
			assertLiveReadImageTerminalContinuation(t, run.events)
			logLiveReadImageEvidence(t, run.capture, observation)
		})
	}
}

type liveReadImageRun struct {
	output  string
	stderr  string
	events  []messages.StreamMessage
	capture gwtesting.SessionCapture
	err     error
}

func runLiveReadImageSession(t *testing.T, apiKey, configDir, prompt string) liveReadImageRun {
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
	stderr := &syncBuffer{}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs([]string{
		"--config-dir", configDir,
		"session",
		"--provider", "openai",
		"--model", liveReadImageModel,
		"--api-key", apiKey,
		"--record", capturePath,
		"--max-duration", liveReadImageTimeout.String(),
		prompt,
	})
	ctx, cancel := context.WithTimeout(context.Background(), liveReadImageTimeout+5*time.Second)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load temporary live read_image capture (run error: %v): %v", runErr, err)
	}
	return liveReadImageRun{
		output:  stdout.String(),
		stderr:  stderr.String(),
		events:  observer.snapshot(),
		capture: capture,
		err:     runErr,
	}
}

type liveReadImageFunctionOutput struct {
	index  int
	callID string
	output string
	result tools.ReadImageResult
}

type liveReadImageTextChunk struct {
	index int
	text  string
}

type liveReadImageResponseDone struct {
	index  int
	status string
}

type liveReadImageWireObservation struct {
	eventTypes            []string
	readImageCallIndex    int
	readImageCallID       string
	readImageCallCount    int
	argumentIndex         int
	argumentCallID        string
	argumentName          string
	argumentPath          string
	argumentCount         int
	functionOutputs       []liveReadImageFunctionOutput
	inputImageCount       int
	correlatedImageCount  int
	correlatedImageIndex  int
	correlatedImageURL    string
	responseCreateIndices []int
	responseDoneEvents    []liveReadImageResponseDone
	sessionClosedIndices  []int
	serverErrorCount      int
	serverErrorTypes      []string
	textChunks            []liveReadImageTextChunk
	continuationIndex     int
	terminalResponseIndex int
	finalText             string
}

// assertLiveReadImageWireContract validates only sanitized, observable facts
// from a temporary raw provider capture. It rejects success for the wrong
// reason: out-of-band pixels, an empty function output, or a function-call-only
// turn cannot satisfy these checks.
func assertLiveReadImageWireContract(t *testing.T, capture gwtesting.SessionCapture, imagePath string, expectedBytes []byte, wantImage bool) liveReadImageWireObservation {
	t.Helper()
	if capture.Provider.Name != "openai" || capture.Provider.Model != liveReadImageModel {
		t.Fatalf("live capture provider = (%q, %q), want (openai, %q)", capture.Provider.Name, capture.Provider.Model, liveReadImageModel)
	}

	observation := liveReadImageWireObservation{
		eventTypes:            make([]string, 0, len(capture.Records)),
		readImageCallIndex:    -1,
		argumentIndex:         -1,
		correlatedImageIndex:  -1,
		continuationIndex:     -1,
		terminalResponseIndex: -1,
	}
	for index, record := range capture.Records {
		prefix := "S"
		if record.Direction == gwtesting.DirectionClientToServer {
			prefix = "C"
		}
		observation.eventTypes = append(observation.eventTypes, prefix+":"+record.Type)
		payload := liveReadImageRecordPayload(record)
		if len(payload) == 0 {
			t.Fatalf("live capture record %d (%s) has an empty payload", index, record.Type)
		}

		if record.Direction == gwtesting.DirectionServerToClient {
			switch record.Type {
			case "response.output_item.added":
				var event struct {
					Item struct {
						Type   string `json:"type"`
						CallID string `json:"call_id"`
						Name   string `json:"name"`
					} `json:"item"`
				}
				liveReadImageUnmarshal(t, payload, &event, "read_image output item")
				if event.Item.Type == "function_call" && event.Item.Name == tools.ReadImageToolID {
					observation.readImageCallCount++
					observation.readImageCallIndex = index
					observation.readImageCallID = event.Item.CallID
				}
			case "response.function_call_arguments.done":
				var event struct {
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}
				liveReadImageUnmarshal(t, payload, &event, "read_image arguments")
				var arguments struct {
					Path string `json:"path"`
				}
				liveReadImageUnmarshal(t, []byte(event.Arguments), &arguments, "read_image argument JSON")
				observation.argumentCount++
				observation.argumentIndex = index
				observation.argumentCallID = event.CallID
				observation.argumentName = event.Name
				observation.argumentPath = arguments.Path
			case "response.output_text.delta", "response.output_audio_transcript.delta":
				var event struct {
					Delta string `json:"delta"`
				}
				liveReadImageUnmarshal(t, payload, &event, "assistant text delta")
				if event.Delta != "" {
					observation.textChunks = append(observation.textChunks, liveReadImageTextChunk{index: index, text: event.Delta})
				}
			case "response.output_audio_transcript.done":
				var event struct {
					Transcript string `json:"transcript"`
				}
				liveReadImageUnmarshal(t, payload, &event, "assistant transcript")
				if event.Transcript != "" {
					observation.textChunks = append(observation.textChunks, liveReadImageTextChunk{index: index, text: event.Transcript})
				}
			case "response.done":
				var event struct {
					Response struct {
						Status string `json:"status"`
					} `json:"response"`
					Status string `json:"status"`
				}
				liveReadImageUnmarshal(t, payload, &event, "response.done")
				status := event.Response.Status
				if status == "" {
					status = event.Status
				}
				observation.responseDoneEvents = append(observation.responseDoneEvents, liveReadImageResponseDone{index: index, status: status})
			case "session.closed":
				observation.sessionClosedIndices = append(observation.sessionClosedIndices, index)
			case "error":
				observation.serverErrorCount++
				observation.serverErrorTypes = append(observation.serverErrorTypes, record.Type)
			}
			continue
		}

		if record.Type == "response.create" {
			observation.responseCreateIndices = append(observation.responseCreateIndices, index)
			continue
		}
		if record.Type != "conversation.item.create" {
			continue
		}
		var event struct {
			Item struct {
				Type     string            `json:"type"`
				CallID   string            `json:"call_id"`
				Output   string            `json:"output"`
				Role     string            `json:"role"`
				Metadata map[string]string `json:"metadata"`
				Content  []struct {
					Type     string `json:"type"`
					ImageURL string `json:"image_url"`
				} `json:"content"`
			} `json:"item"`
		}
		liveReadImageUnmarshal(t, payload, &event, "conversation item")
		switch event.Item.Type {
		case "function_call_output":
			if strings.TrimSpace(event.Item.Output) == "" {
				t.Fatal("live function_call_output output is empty")
			}
			var result tools.ReadImageResult
			liveReadImageUnmarshal(t, []byte(event.Item.Output), &result, "read_image result envelope")
			observation.functionOutputs = append(observation.functionOutputs, liveReadImageFunctionOutput{
				index:  index,
				callID: event.Item.CallID,
				output: event.Item.Output,
				result: result,
			})
		case "message":
			for _, part := range event.Item.Content {
				if part.Type != "input_image" {
					continue
				}
				observation.inputImageCount++
				if event.Item.Metadata["tool_call_id"] == observation.readImageCallID {
					observation.correlatedImageCount++
					observation.correlatedImageIndex = index
					observation.correlatedImageURL = part.ImageURL
				}
			}
		}
	}

	if observation.readImageCallCount != 1 || observation.readImageCallID == "" {
		t.Fatalf("live read_image calls = %d, call ID %q; want exactly one real model call", observation.readImageCallCount, observation.readImageCallID)
	}
	if observation.argumentCount != 1 || observation.argumentCallID != observation.readImageCallID || observation.argumentName != tools.ReadImageToolID || observation.argumentPath != imagePath {
		t.Fatalf("live read_image arguments = count %d, call %q, name %q, path %q; want one correlated call to %q at the requested path", observation.argumentCount, observation.argumentCallID, observation.argumentName, observation.argumentPath, imagePath)
	}
	if len(observation.functionOutputs) != 1 {
		t.Fatalf("live function_call_output count = %d, want exactly one", len(observation.functionOutputs))
	}
	functionOutput := observation.functionOutputs[0]
	if functionOutput.callID != observation.readImageCallID {
		t.Fatalf("live function_call_output call ID = %q, want %q", functionOutput.callID, observation.readImageCallID)
	}
	assertLiveReadImageEnvelope(t, functionOutput, expectedBytes, wantImage)

	if observation.serverErrorCount != 0 {
		t.Fatalf("live capture contains provider error events: %v", observation.serverErrorTypes)
	}
	if len(observation.responseCreateIndices) != 2 {
		t.Fatalf("live response.create count = %d, want initial request plus exactly one continuation", len(observation.responseCreateIndices))
	}
	if observation.readImageCallIndex <= observation.responseCreateIndices[0] {
		t.Fatalf("live read_image call index = %d, initial response.create index = %d; call must follow the initial response request", observation.readImageCallIndex, observation.responseCreateIndices[0])
	}
	observation.continuationIndex = observation.responseCreateIndices[1]
	if functionOutput.index >= observation.continuationIndex {
		t.Fatalf("live function_call_output index = %d, continuation response.create index = %d; result must precede continuation", functionOutput.index, observation.continuationIndex)
	}
	if wantImage {
		if observation.inputImageCount != 1 || observation.correlatedImageCount != 1 || observation.correlatedImageIndex <= functionOutput.index {
			t.Fatalf("live input_image counts = total %d, correlated %d, index %d; want one correlated image after function output", observation.inputImageCount, observation.correlatedImageCount, observation.correlatedImageIndex)
		}
		wantURL := liveReadImageDataURL(expectedBytes)
		if observation.correlatedImageURL != wantURL {
			t.Fatalf("live correlated input_image did not preserve the exact fixture bytes (URL length=%d, want length=%d)", len(observation.correlatedImageURL), len(wantURL))
		}
		if observation.correlatedImageIndex >= observation.continuationIndex {
			t.Fatalf("live correlated input_image index = %d, continuation response.create index = %d; image must precede continuation", observation.correlatedImageIndex, observation.continuationIndex)
		}
	} else if observation.inputImageCount != 0 {
		t.Fatalf("live missing-image result emitted %d input_image item(s), want none", observation.inputImageCount)
	}

	for _, done := range observation.responseDoneEvents {
		if done.index <= observation.continuationIndex {
			continue
		}
		if done.status == "cancelled" || done.status == "failed" || done.status == "incomplete" {
			t.Fatalf("live continuation response.done status = %q at record %d", done.status, done.index)
		}
		observation.terminalResponseIndex = done.index
		break
	}
	if observation.terminalResponseIndex < 0 {
		t.Fatalf("live capture has no terminal response.done after continuation response.create")
	}
	for _, closedIndex := range observation.sessionClosedIndices {
		if closedIndex < observation.terminalResponseIndex {
			t.Fatalf("live provider session.closed at record %d preceded terminal continuation response.done at record %d", closedIndex, observation.terminalResponseIndex)
		}
	}

	var finalText strings.Builder
	for _, chunk := range observation.textChunks {
		if chunk.index > observation.continuationIndex {
			finalText.WriteString(chunk.text)
		}
	}
	observation.finalText = finalText.String()
	if strings.TrimSpace(observation.finalText) == "" {
		t.Fatal("live continuation emitted no assistant text after the accepted tool result")
	}
	finalTextIndex := -1
	for _, chunk := range observation.textChunks {
		if chunk.index > observation.continuationIndex {
			finalTextIndex = chunk.index
			break
		}
	}
	if finalTextIndex <= observation.continuationIndex || finalTextIndex > observation.terminalResponseIndex {
		t.Fatalf("live final assistant text index = %d, continuation = %d, terminal response = %d", finalTextIndex, observation.continuationIndex, observation.terminalResponseIndex)
	}
	return observation
}

func assertLiveReadImageEnvelope(t *testing.T, output liveReadImageFunctionOutput, expectedBytes []byte, wantImage bool) {
	t.Helper()
	result := output.result
	if result.Version != tools.ReadImageResultVersion {
		t.Fatalf("live read_image result version = %d, want %d", result.Version, tools.ReadImageResultVersion)
	}
	if wantImage {
		if result.Status != tools.ReadImageResultStatusSuccess {
			t.Fatalf("live readable read_image result status = %q, want success", result.Status)
		}
		wantDigest := sha256.Sum256(expectedBytes)
		wantDigestHex := hex.EncodeToString(wantDigest[:])
		wantURL := liveReadImageDataURL(expectedBytes)
		if result.MIMEType != "image/png" || result.ByteLength != len(expectedBytes) || result.SHA256 != wantDigestHex || result.DataURL != wantURL {
			t.Fatalf("live success result metadata = (MIME=%q length=%d digest=%q data_url_length=%d), want exact PNG bytes", result.MIMEType, result.ByteLength, result.SHA256, len(result.DataURL))
		}
		encoded := strings.TrimPrefix(result.DataURL, "data:image/png;base64,")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || !bytes.Equal(decoded, expectedBytes) {
			t.Fatalf("live success data URL does not decode to the committed fixture bytes: decode error=%v decoded bytes=%d want=%d", err, len(decoded), len(expectedBytes))
		}
		return
	}

	if result.Status != tools.ReadImageResultStatusError || strings.TrimSpace(result.Error) == "" {
		t.Fatalf("live missing read_image result = %#v, want a non-empty versioned error envelope", result)
	}
	errorText := strings.ToLower(result.Error)
	if !strings.Contains(errorText, "missing") && !strings.Contains(errorText, "no such file") && !strings.Contains(errorText, "not exist") {
		t.Fatalf("live missing read_image error = %q, want a missing-file explanation", result.Error)
	}
	if result.MIMEType != "" || result.ByteLength != 0 || result.SHA256 != "" || result.DataURL != "" || strings.Contains(strings.ToLower(output.output), "data:") {
		t.Fatalf("live missing read_image result unexpectedly carried image data: metadata=(MIME=%q length=%d digest=%q data_url_length=%d)", result.MIMEType, result.ByteLength, result.SHA256, len(result.DataURL))
	}
}

func assertLiveReadImageSemanticResult(t *testing.T, cliOutput, continuationText string, wantImage bool) {
	t.Helper()
	cliText := strings.ToLower(cliOutput)
	finalText := strings.ToLower(continuationText)
	if wantImage {
		for _, text := range []struct {
			name  string
			value string
		}{
			{name: "pixel", value: "pixel"},
			{name: "indigo", value: "indigo"},
		} {
			if !strings.Contains(finalText, text.value) || !strings.Contains(cliText, text.value) {
				t.Fatalf("live readable continuation/CLI output missing grounded %s fact (continuation=%q cli_output=%q)", text.name, continuationText, cliOutput)
			}
		}
		if !strings.Contains(finalText, "1") && !strings.Contains(finalText, "one") {
			t.Fatalf("live readable continuation did not report the asserted one-pixel dimension: %q", continuationText)
		}
		return
	}

	if strings.Contains(finalText, "indigo") || strings.Contains(finalText, "one-by-one") {
		t.Fatalf("live missing continuation fabricated readable-image grounding: %q", continuationText)
	}
	if !strings.Contains(finalText, "image") {
		t.Fatalf("live missing continuation did not mention the image: %q", continuationText)
	}
	if !strings.Contains(finalText, "could") && !strings.Contains(finalText, "unable") && !strings.Contains(finalText, "cannot") && !strings.Contains(finalText, "can't") && !strings.Contains(finalText, "failed") {
		t.Fatalf("live missing continuation did not explain the read failure: %q", continuationText)
	}
	if !strings.Contains(finalText, "missing") && !strings.Contains(finalText, "not found") && !strings.Contains(finalText, "no such") && !strings.Contains(finalText, "does not exist") {
		t.Fatalf("live missing continuation did not identify the missing-file cause: %q", continuationText)
	}
	if strings.Contains(cliText, "use of closed network connection") || strings.Contains(cliText, "closed network connection") {
		t.Fatalf("live missing CLI output contains a closed-network error: %q", cliOutput)
	}
}

func assertLiveReadImageTerminalContinuation(t *testing.T, events []messages.StreamMessage) {
	t.Helper()
	toolCallIndex := -1
	finalAssistantEnd := -1
	for index, event := range events {
		if value, ok := event.Value.(*messages.ToolCallEndValue); ok && value != nil && value.Name == tools.ReadImageToolID {
			if toolCallIndex >= 0 {
				t.Fatalf("live observer saw duplicate read_image tool calls")
			}
			toolCallIndex = index
		}
		if event.Type == messages.StreamTypeMessageEnd && event.Role != messages.RoleTool && toolCallIndex >= 0 {
			finalAssistantEnd = index
		}
	}
	if toolCallIndex < 0 || finalAssistantEnd <= toolCallIndex {
		t.Fatalf("live observer did not see a terminal assistant continuation after read_image: tool_call=%d assistant_end=%d", toolCallIndex, finalAssistantEnd)
	}
}

func logLiveReadImageEvidence(t *testing.T, capture gwtesting.SessionCapture, observation liveReadImageWireObservation) {
	t.Helper()
	result := observation.functionOutputs[0].result
	eventOrder := make([]string, 0, len(observation.eventTypes))
	for _, eventType := range observation.eventTypes {
		if strings.Contains(eventType, "response.output_audio.delta") {
			continue
		}
		eventOrder = append(eventOrder, eventType)
	}
	t.Logf("sanitized live read_image evidence: started_at_utc=%s model=%s result_status=%s result_bytes=%d result_sha256=%s input_image_count=%d response_create_count=%d terminal_response_done=true exit=0 event_order=%s", capture.Session.StartedAtUTC, capture.Provider.Model, result.Status, result.ByteLength, result.SHA256, observation.inputImageCount, len(observation.responseCreateIndices), strings.Join(eventOrder, ">"))
}

func liveReadImageDataURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func liveReadImageRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func liveReadImageUnmarshal(t *testing.T, payload []byte, destination any, description string) {
	t.Helper()
	if err := json.Unmarshal(payload, destination); err != nil {
		t.Fatalf("decode live %s: %v", description, err)
	}
}
