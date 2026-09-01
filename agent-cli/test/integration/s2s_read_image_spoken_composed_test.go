package integration

// Story 004 proves the complete credential-free voice-to-vision composition.
// The public session command receives a file-backed spoken request, the
// production tool executor reads one deterministic image, and strict replay
// gates the grounded continuation on the compact result plus one typed image
// projection. The negative control keeps that accepted transaction intact but
// supplies an empty token-limit-style provider failure.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	readImageSpokenModel          = "gpt-realtime"
	readImageSpokenTextBudget     = 1024
	readImageSpokenFailedResponse = `{"type":"response.done","response":{"id":"resp_read_image_reply","status":"failed","status_details":{"type":"token_limit","reason":"max_output_tokens","code":"token_limit_exceeded","message":"continuation exceeded token limit"}}}`
)

type readImageSpokenRun struct {
	stdout string
	stderr string
	events []messages.StreamMessage
	err    error
}

// buildSpokenReadImageFixture turns the redacted text-seeded read_image
// capture into the exact client-side exchange produced by --audio-in. The
// committed spoken corpus is injected at runtime, while the capture remains
// free of raw audio and image bytes. When failed is true, assistant output
// after the continuation response.created event is omitted and its response
// terminal is changed to a bounded token-limit failure.
func buildSpokenReadImageFixture(t *testing.T, committedPath string, wavPath string, failed bool) string {
	t.Helper()
	capture := captureCopy(t, committedPath)
	frames := multiturnAudioFrames(t, wavPath)
	if len(frames) == 0 {
		t.Fatal("spoken read_image input WAV produced no PCM frames")
	}

	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(capture.Records)+len(frames)+2)
	seedReplaced := false
	seedResponseCreateSkipped := false
	responseCreated := 0
	responseDone := 0
	continuationStarted := false
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "session.closed" {
			// The finite audio session owns its completion boundary. Leaving a
			// provider close in the capture would let a close-only path mask a
			// failed or empty continuation.
			continue
		}

		if !seedReplaced && isReadImageSeedMessage(record) {
			seedReplaced = true
			for _, frame := range frames {
				records = append(records, readImageSpokenClientEvent(t, "input_audio_buffer.append", map[string]string{
					"type":  "input_audio_buffer.append",
					"audio": base64.StdEncoding.EncodeToString(frame),
				}))
			}
			records = append(records,
				readImageSpokenClientEvent(t, "input_audio_buffer.commit", map[string]string{
					"type": "input_audio_buffer.commit",
				}),
				readImageSpokenClientEvent(t, "response.create", map[string]string{
					"type": "response.create",
				}),
			)
			continue
		}
		if seedReplaced && !seedResponseCreateSkipped && record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "response.create" {
			// The text-seeded capture owns a response.create for the removed
			// conversation item. The audio end-of-turn above supplies its one
			// replacement; retain only the later tool continuation request.
			seedResponseCreateSkipped = true
			continue
		}

		if failed && record.Direction == gatewaytesting.DirectionServerToClient {
			switch record.Type {
			case "response.created":
				responseCreated++
				if responseCreated == 2 {
					continuationStarted = true
				}
			case "response.output_text.delta", "response.output_text.done":
				if continuationStarted {
					continue
				}
			case "response.done":
				responseDone++
				if responseDone == 2 {
					record.Payload = json.RawMessage(readImageSpokenFailedResponse)
					record.Data = nil
				}
			}
		}
		records = append(records, record)
	}
	if !seedReplaced {
		t.Fatal("read_image capture has no seed message to replace with spoken input")
	}
	if failed && (responseCreated != 2 || responseDone != 2) {
		t.Fatalf("failed spoken fixture saw response.created=%d response.done=%d, want two of each", responseCreated, responseDone)
	}

	sequence := 0
	capture.Records = resequencedBatch(records, &sequence)
	path := filepath.Join(t.TempDir(), "read-image-spoken.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal spoken read_image fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write spoken read_image fixture: %v", err)
	}
	if _, err := gatewaytesting.NewReplayWebSocketDialer(path); err != nil {
		t.Fatalf("spoken read_image fixture rejected by strict replay: %v", err)
	}
	return path
}

func isReadImageSeedMessage(record gatewaytesting.CapturedSessionEvent) bool {
	if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != "conversation.item.create" {
		return false
	}
	var payload struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal(readImageSpokenRecordPayload(record), &payload); err != nil {
		return false
	}
	return payload.Item.Type == "message"
}

func readImageSpokenRecordPayload(record gatewaytesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func readImageSpokenClientEvent(t *testing.T, eventType string, payload map[string]string) gatewaytesting.CapturedSessionEvent {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s event: %v", eventType, err)
	}
	return gatewaytesting.CapturedSessionEvent{
		Direction:   gatewaytesting.DirectionClientToServer,
		Type:        eventType,
		PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload:     data,
	}
}

// assertSpokenReadImageWireContract adds the voice-turn assertions to the
// existing read_image wire oracle. Strict replay uses the same materialized
// capture as the CLI run, so an oversized output or duplicated image fails at
// the outbound boundary before the scripted assistant answer is released.
func assertSpokenReadImageWireContract(t *testing.T, fixturePath, wavPath, imagePath string, expectedBytes []byte) {
	t.Helper()
	assertReadImageWireContract(t, fixturePath, imagePath, expectedBytes)

	frames := multiturnAudioFrames(t, wavPath)
	capture := captureCopy(t, fixturePath)
	appendCount := 0
	commitCount := 0
	responseCreates := make([]int, 0, 2)
	lastAppend := -1
	commitIndex := -1
	functionOutputIndex := -1
	imageIndex := -1
	for index, record := range capture.Records {
		payload := readImageSpokenRecordPayload(record)
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		switch record.Type {
		case "input_audio_buffer.append":
			var event struct {
				Audio string `json:"audio"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode spoken audio append %d: %v", appendCount+1, err)
			}
			if appendCount >= len(frames) {
				t.Fatalf("spoken audio append count exceeded WAV frame count %d", len(frames))
			}
			decoded, err := base64.StdEncoding.DecodeString(event.Audio)
			if err != nil {
				t.Fatalf("decode spoken audio append %d: %v", appendCount+1, err)
			}
			if string(decoded) != string(frames[appendCount]) {
				t.Fatalf("spoken audio frame %d changed before provider delivery", appendCount+1)
			}
			appendCount++
			lastAppend = index
		case "input_audio_buffer.commit":
			commitCount++
			commitIndex = index
		case "response.create":
			responseCreates = append(responseCreates, index)
		case "conversation.item.create":
			var event struct {
				Item struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Output string `json:"output"`
					ID     string `json:"id"`
				} `json:"item"`
			}
			if err := json.Unmarshal(payload, &event); err != nil {
				t.Fatalf("decode spoken conversation item: %v", err)
			}
			switch event.Item.Type {
			case "function_call_output":
				functionOutputIndex = index
				if len(event.Item.Output) > readImageSpokenTextBudget {
					t.Fatalf("spoken function output is %d bytes, want <= %d", len(event.Item.Output), readImageSpokenTextBudget)
				}
			case "message":
				if event.Item.ID == readImageToolImageItemID(readImageCallID) {
					imageIndex = index
				}
			}
		}
	}
	if appendCount != len(frames) {
		t.Fatalf("spoken audio append count = %d, want exactly %d WAV frames", appendCount, len(frames))
	}
	if commitCount != 1 || lastAppend < 0 || commitIndex <= lastAppend {
		t.Fatalf("spoken audio boundary = appends %d last=%d commits %d at %d, want one commit after all frames", appendCount, lastAppend, commitCount, commitIndex)
	}
	if len(responseCreates) != 2 || responseCreates[0] <= commitIndex {
		t.Fatalf("spoken response.create indices = %v, want initial request after audio commit plus one continuation", responseCreates)
	}
	if functionOutputIndex < 0 || imageIndex <= functionOutputIndex || responseCreates[1] <= imageIndex {
		t.Fatalf("spoken read_image order = output %d, image %d, continuation %d; want output < image < continuation", functionOutputIndex, imageIndex, responseCreates[1])
	}
}

func assertReadImageFailedContinuationFixture(t *testing.T, fixturePath string) {
	t.Helper()
	capture := captureCopy(t, fixturePath)
	responseDone := 0
	continuationOutput := false
	continuationStarted := false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.created":
			if continuationStarted {
				t.Fatalf("failed continuation fixture contains more than two response.created events")
			}
			if !continuationStarted {
				continuationStarted = responseDone == 1
			}
		case "response.output_text.delta", "response.output_text.done":
			if continuationStarted {
				continuationOutput = true
			}
		case "response.done":
			responseDone++
			if responseDone == 2 {
				var payload struct {
					Response struct {
						Status        string `json:"status"`
						StatusDetails struct {
							Type    string `json:"type"`
							Reason  string `json:"reason"`
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"status_details"`
					} `json:"response"`
				}
				if err := json.Unmarshal(readImageSpokenRecordPayload(record), &payload); err != nil {
					t.Fatalf("decode failed continuation terminal: %v", err)
				}
				if payload.Response.Status != "failed" || payload.Response.StatusDetails.Type != "token_limit" || payload.Response.StatusDetails.Code != "token_limit_exceeded" {
					t.Fatalf("failed continuation terminal = %#v, want token-limit failure", payload.Response)
				}
				if !strings.Contains(payload.Response.StatusDetails.Reason, "max_output_tokens") || !strings.Contains(payload.Response.StatusDetails.Message, "token limit") {
					t.Fatalf("failed continuation details = %#v, want actionable token-limit details", payload.Response.StatusDetails)
				}
			}
		}
	}
	if responseDone != 2 {
		t.Fatalf("failed continuation fixture has %d response.done events, want two", responseDone)
	}
	if continuationOutput {
		t.Fatal("failed continuation fixture contains assistant output after the failed terminal")
	}
}

func runSpokenReadImageSession(t *testing.T, fixturePath, configDir, imagePath, wavPath string) readImageSpokenRun {
	t.Helper()
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
		"--workdir", filepath.Dir(imagePath),
		"session",
		"--replay", fixturePath,
		"--provider", "openai",
		"--model", readImageSpokenModel,
		"--audio-in", wavPath,
		"--max-duration", "5s",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	return readImageSpokenRun{
		stdout: stdout.String(),
		stderr: stderr.String(),
		events: observer.snapshot(),
		err:    runErr,
	}
}

func assertReadImageContinuationFailure(t *testing.T, run readImageSpokenRun) {
	t.Helper()
	if run.err == nil {
		t.Fatalf("failed empty continuation completed cleanly\nstdout:\n%s\nstderr:\n%s", run.stdout, run.stderr)
	}
	if !errors.Is(run.err, services.ErrSessionImageContinuationIncomplete) {
		t.Fatalf("failed empty continuation error = %v, want image continuation sentinel", run.err)
	}
	var continuationErr *services.SessionImageContinuationError
	if !errors.As(run.err, &continuationErr) {
		t.Fatalf("failed empty continuation error = %v, want typed image continuation error", run.err)
	}
	if len(continuationErr.CallIDs) != 1 || continuationErr.CallIDs[0] != readImageCallID {
		t.Fatalf("failed continuation call IDs = %v, want [%s]", continuationErr.CallIDs, readImageCallID)
	}
	if continuationErr.ProviderStatuses[readImageCallID] != "failed" {
		t.Fatalf("failed continuation status = %q, want failed", continuationErr.ProviderStatuses[readImageCallID])
	}
	if !strings.Contains(continuationErr.ProviderDetails[readImageCallID], "token_limit") && !strings.Contains(continuationErr.ProviderDetails[readImageCallID], "max_output_tokens") {
		t.Fatalf("failed continuation detail = %q, want token-limit provider context", continuationErr.ProviderDetails[readImageCallID])
	}
	for _, marker := range readImageGroundedMarkers {
		if strings.Contains(strings.ToLower(run.stdout+"\n"+run.stderr), marker) {
			t.Fatalf("failed empty continuation fabricated grounded marker %q", marker)
		}
	}
	if strings.Contains(strings.ToLower(run.stdout+"\n"+run.stderr), "[session closed: client_close]") {
		t.Fatalf("failed empty continuation was reported as clean client_close:\nstdout:\n%s\nstderr:\n%s", run.stdout, run.stderr)
	}
	if !strings.Contains(run.err.Error(), readImageCallID) || !strings.Contains(run.err.Error(), "failed") {
		t.Fatalf("failed empty continuation diagnostic = %v, want call identity and failed status", run.err)
	}
}

func assertReadImageSpokenSuccessLifecycle(t *testing.T, events []messages.StreamMessage) {
	t.Helper()
	toolCalls := 0
	imageStarts := 0
	imageDeltas := 0
	imageEnds := 0
	imageEndIndex := -1
	finalAssistantEnd := -1
	for index, event := range events {
		switch event.Type {
		case messages.StreamTypeToolCallEnd:
			if value, ok := event.Value.(*messages.ToolCallEndValue); ok && value != nil && value.Name == "read_image" {
				toolCalls++
			}
		case messages.StreamTypeImageStart:
			if event.Role == messages.RoleTool {
				imageStarts++
			}
		case messages.StreamTypeImageDelta:
			if event.Role == messages.RoleTool {
				imageDeltas++
			}
		case messages.StreamTypeImageEnd:
			if event.Role == messages.RoleTool {
				imageEnds++
				imageEndIndex = index
			}
		case messages.StreamTypeMessageEnd:
			if event.Role == messages.RoleTool || index <= imageEndIndex {
				continue
			}
			terminal, ok := event.Value.(*messages.MessageEndValue)
			if !ok || terminal == nil || terminal.Status != "completed" {
				continue
			}
			finalAssistantEnd = index
		}
	}
	if toolCalls != 1 {
		t.Fatalf("spoken success observed %d read_image tool calls, want exactly one", toolCalls)
	}
	if imageStarts != 1 || imageDeltas == 0 || imageEnds != 1 {
		t.Fatalf("spoken success image lifecycle = starts %d, deltas %d, ends %d; want one complete typed image", imageStarts, imageDeltas, imageEnds)
	}
	if finalAssistantEnd <= imageEndIndex {
		t.Fatalf("spoken success has no completed assistant terminal after image: image_end=%d assistant_end=%d events=%#v", imageEndIndex, finalAssistantEnd, events)
	}
}

func assertReadImageSpokenFailureLifecycle(t *testing.T, events []messages.StreamMessage) {
	t.Helper()
	toolCalls := 0
	imageStarts := 0
	imageDeltas := 0
	imageEnds := 0
	failedTerminal := -1
	for index, event := range events {
		switch event.Type {
		case messages.StreamTypeToolCallEnd:
			if value, ok := event.Value.(*messages.ToolCallEndValue); ok && value != nil && value.Name == "read_image" {
				toolCalls++
			}
		case messages.StreamTypeImageStart:
			if event.Role == messages.RoleTool {
				imageStarts++
			}
		case messages.StreamTypeImageDelta:
			if event.Role == messages.RoleTool {
				imageDeltas++
			}
		case messages.StreamTypeImageEnd:
			if event.Role == messages.RoleTool {
				imageEnds++
			}
		case messages.StreamTypeMessageEnd:
			terminal, ok := event.Value.(*messages.MessageEndValue)
			if ok && terminal != nil && terminal.Status == "failed" {
				failedTerminal = index
			}
		case messages.StreamTypeTextDelta, messages.StreamTypeTranscriptDelta, messages.StreamTypeAudioDelta:
			if failedTerminal >= 0 && event.Role != messages.RoleTool {
				t.Fatalf("failed spoken continuation emitted assistant output after failed terminal at %d: event %d=%#v", failedTerminal, index, event)
			}
		}
	}
	if toolCalls != 1 {
		t.Fatalf("spoken failure observed %d read_image tool calls, want exactly one", toolCalls)
	}
	if imageStarts != 1 || imageDeltas == 0 || imageEnds != 1 {
		t.Fatalf("spoken failure image lifecycle = starts %d, deltas %d, ends %d; want one complete typed image", imageStarts, imageDeltas, imageEnds)
	}
	if failedTerminal < 0 {
		t.Fatalf("spoken failure observed no provider failed terminal: events=%#v", events)
	}
}

// TestReadImageSpokenProductionComposition proves the positive production
// path: spoken input, one default read_image execution, one compact result,
// one exact typed image, and a grounded assistant continuation.
func TestReadImageSpokenProductionComposition(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "photo.png")
	imageBytes := readImageFixtureBytes(t)
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatalf("write deterministic spoken-flow image: %v", err)
	}
	if err := assertReadImageFixturePixels(imageBytes); err != nil {
		t.Fatal(err)
	}
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	configDir := writeReadImageModelConfig(t, true, readImageSpokenModel)
	materialized := materializeReadImageReplayFixture(t, readImageReplayFixturePath(t, readImagePositiveFixtureName), imagePath, imageBytes)
	fixture := buildSpokenReadImageFixture(t, materialized, wavPath, false)
	assertSpokenReadImageWireContract(t, fixture, wavPath, imagePath, imageBytes)

	run := runSpokenReadImageSession(t, fixture, configDir, imagePath, wavPath)
	if run.err != nil {
		t.Fatalf("spoken read_image composition failed: %v\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
	}
	if err := assertReadImageGroundedWithProviderClose(run.stdout, run.events, imagePath, imageBytes, false); err != nil {
		t.Fatal(err)
	}
	assertReadImageSpokenSuccessLifecycle(t, run.events)
}

// TestReadImageSpokenFailedContinuationIsActionable is the negative control:
// the strict provider accepts the image transaction, then returns no
// assistant output and a token-limit-style failed response. The CLI must
// return the typed continuation failure rather than a clean close.
func TestReadImageSpokenFailedContinuationIsActionable(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "photo.png")
	imageBytes := readImageFixtureBytes(t)
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatalf("write deterministic failed-flow image: %v", err)
	}
	if err := assertReadImageFixturePixels(imageBytes); err != nil {
		t.Fatal(err)
	}
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	configDir := writeReadImageModelConfig(t, true, readImageSpokenModel)
	materialized := materializeReadImageReplayFixture(t, readImageReplayFixturePath(t, readImagePositiveFixtureName), imagePath, imageBytes)
	fixture := buildSpokenReadImageFixture(t, materialized, wavPath, true)
	assertSpokenReadImageWireContract(t, fixture, wavPath, imagePath, imageBytes)
	assertReadImageFailedContinuationFixture(t, fixture)

	run := runSpokenReadImageSession(t, fixture, configDir, imagePath, wavPath)
	assertReadImageContinuationFailure(t, run)
	assertReadImageSpokenFailureLifecycle(t, run.events)
}

// TestReadImageSpokenStrictReplayRejectsUnboundedAndDuplicatedPixels proves
// the provider seam is a gate, not a post-run inspection. Both malformed
// expected transactions stop the same production CLI before the scripted
// grounded response can be released.
func TestReadImageSpokenStrictReplayRejectsUnboundedAndDuplicatedPixels(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "photo.png")
	imageBytes := readImageFixtureBytes(t)
	if err := os.WriteFile(imagePath, imageBytes, 0o600); err != nil {
		t.Fatalf("write deterministic strict-gate image: %v", err)
	}
	wavPath := locateCLIFixture(t, visionDescribeQuestionWAV)
	configDir := writeReadImageModelConfig(t, true, readImageSpokenModel)
	validMaterialized := materializeReadImageReplayFixture(t, readImageReplayFixturePath(t, readImagePositiveFixtureName), imagePath, imageBytes)
	validFixture := buildSpokenReadImageFixture(t, validMaterialized, wavPath, false)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)

	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "oversized function output",
			mutate: func(item map[string]any) {
				item["output"] = strings.Repeat("x", readImageSpokenTextBudget+1)
			},
		},
		{
			name: "pixels duplicated in function output",
			mutate: func(item map[string]any) {
				item["output"] = item["output"].(string) + dataURL
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := rewriteReadImageCapture(t, validFixture, testCase.mutate)
			run := runSpokenReadImageSession(t, fixture, configDir, imagePath, wavPath)
			if run.err == nil {
				t.Fatalf("strict replay control completed cleanly; malformed provider transaction was accepted\nstdout:\n%s", run.stdout)
			}
			if !errors.Is(run.err, providers.ErrReplayMismatch) {
				t.Fatalf("strict replay control error = %v, want replay mismatch at provider result gate", run.err)
			}
			for _, marker := range readImageGroundedMarkers {
				if strings.Contains(strings.ToLower(run.stdout+"\n"+run.stderr), marker) {
					t.Fatalf("strict replay control released grounded marker %q after malformed result", marker)
				}
			}
		})
	}
}
