package integration

// s2s-e2e-tool-call-conversation depth-5 vertical: CLI-verified hermetic proof
// that a customer's spoken request drives exactly one named CLI tool call and
// the agent's spoken reply reflects what that tool ACTUALLY returned.
//
// Proven through the public 'agent session' CLI surface over the record/replay
// WebSocket transport:
//   - an injected messages.ToolExecutor observes exactly one call with the
//     expected name and arguments,
//   - the provider exchange carries the executor's runtime return value
//     verbatim as the conversation.item.create function_call_output event,
//   - the fixture's server-side transcript deltas are authored from that same
//     runtime return value and are delivered ONLY after the replay transport
//     validates the outbound function_call_output frame, so a spoken reply
//     quoting values unique to the result ("24 degrees", "clear skies") is
//     causally downstream of the real tool execution,
//   - the recorded --audio-out WAV passes RMS > 500 speech assertions with
//     duration bounds,
//   - a negative control whose executor returns different content fails
//     deterministically on the transcript-content assertion via replay
//     divergence — never by timeout.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// toolConversationCallID is the call_id shared by the scripted provider tool
// call and the expected function_call_output pairing.
const toolConversationCallID = "call_weather_1"

// toolResultPositive is the weather report the positive-path executor returns
// at runtime. The asserted transcript values "24 degrees" and "clear skies"
// are derived from this payload and appear nowhere else in the exchange.
const toolResultPositive = `{"temperature_c":24,"condition":"clear"}`

// toolResultControl differs from the fixture-authored expectation, proving the
// reflection assertion cannot pass when the tool returns different content.
const toolResultControl = `{"temperature_c":-5,"condition":"stormy"}`

// conversationWeatherResult mirrors the executor's ToolCallResponse content.
type conversationWeatherResult struct {
	TemperatureC int    `json:"temperature_c"`
	Condition    string `json:"condition"`
}

// parseToolResult decodes one executor runtime return value.
func parseToolResult(t *testing.T, raw string) conversationWeatherResult {
	t.Helper()
	var parsed conversationWeatherResult
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("parse tool result %s: %v", raw, err)
	}
	return parsed
}

// spokenReplyFor authors the server-side spoken reply FROM the executor's
// runtime return value: the transcript literally quotes what the tool
// reported. The reply markers ("24 degrees", "clear skies") are unique to the
// result payload — they never appear in the raw JSON itself nor anywhere else
// in the exchange.
func spokenReplyFor(result conversationWeatherResult) string {
	return fmt.Sprintf("The weather in Lisbon is %d degrees with %s skies.", result.TemperatureC, result.Condition)
}

// conversationResultExecutor records every invocation and the exact content it
// returned so the test can bind wire and transcript observations to the
// executor's runtime behavior.
type conversationResultExecutor struct {
	mu       sync.Mutex
	result   string
	calls    []messages.ToolCall
	returned []string
}

func (e *conversationResultExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call)
	e.returned = append(e.returned, e.result)
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: e.result}, nil
}

func (e *conversationResultExecutor) snapshot() (calls []messages.ToolCall, returned []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...), append([]string(nil), e.returned...)
}

// buildToolResultConversationFixture writes a synthetic record/replay capture
// whose causal chain proves the spoken reply reflects the real tool result:
// the client-to-server side expects the paced input-audio frames plus commit
// and response.create, then — only after the named function tool call — one
// conversation.item.create carrying function_call_output with the executor's
// expected output; the server-to-client side delivers the transcript deltas
// (authored from that same output) and voiced audio strictly AFTER that gated
// frame. The fixture is synthetic-provenance tagged and validated by the
// replay dialer before use.
func buildToolResultConversationFixture(t *testing.T, wavPath string, replySamples []int16, toolResultOutput string, includeToolCall bool) string {
	t.Helper()
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read input WAV fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse input WAV fixture: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("input WAV rate = %d, want %d", rate, audio.SampleRate)
	}

	baseCapture, err := gwtesting.LoadSessionCapture(filepath.Join("testdata", "openai_realtime_smoke.session.json"))
	if err != nil {
		t.Fatalf("load replay base fixture: %v", err)
	}
	records := []gwtesting.CapturedSessionEvent{baseCapture.Records[0], baseCapture.Records[1]}

	clientEvent := func(eventType string, payload json.RawMessage) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		})
	}

	frame := make([]int16, audio.FrameSize)
	for start := 0; start < len(samples); start += audio.FrameSize {
		clear(frame)
		copy(frame, samples[start:])
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(pcm16LEBytes(frame)),
		})
		if marshalErr != nil {
			t.Fatalf("marshal append event: %v", marshalErr)
		}
		clientEvent("input_audio_buffer.append", payload)
	}
	clientEvent("input_audio_buffer.commit", json.RawMessage(`{"type":"input_audio_buffer.commit"}`))
	clientEvent("response.create", json.RawMessage(`{"type":"response.create"}`))

	serverEvent := func(eventType string, payload string) {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionServerToClient,
			TimestampMs: int64(len(records)),
			Type:        eventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(payload),
		})
	}

	spokenReply := spokenReplyFor(parseToolResult(t, toolResultOutput))

	serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_tool_result_conversation"}}`)
	if includeToolCall {
		serverEvent("response.output_item.added",
			`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"`+toolConversationCallID+`","name":"`+toolCallScenarioName+`"}}`)
		serverEvent("response.function_call_arguments.done",
			`{"type":"response.function_call_arguments.done","call_id":"`+toolConversationCallID+`","name":"`+toolCallScenarioName+`","arguments":`+strconvQuote(toolCallScenarioArguments)+`}`)
		// The tool-call response terminates with the call pending; the
		// spoken follow-up response exists only after the executed result is
		// delivered back to the provider.
		serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_tool_result_conversation","status":"completed"}}`)

		// The gating frame: replay validation blocks every later inbound
		// record until the live session sends this exact function_call_output
		// carrying the executor's runtime return value. A differing executor
		// result diverges the replay here, deterministically withholding the
		// spoken reply.
		outputPayload, outputMarshalErr := json.Marshal(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]string{
				"type":    "function_call_output",
				"call_id": toolConversationCallID,
				"output":  toolResultOutput,
			},
		})
		if outputMarshalErr != nil {
			t.Fatalf("marshal function_call_output event: %v", outputMarshalErr)
		}
		clientEvent("conversation.item.create", outputPayload)
		// The function_call_output item is not itself a response boundary.
		// Realtime must receive one explicit response.create after the complete
		// result batch before the grounded spoken continuation can begin.
		clientEvent("response.create", json.RawMessage(`{"type":"response.create"}`))

		serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_tool_result_conversation_reply"}}`)
	}

	// Server-side transcript deltas authored from the executor's runtime
	// return value, split across two deltas so the full text only exists
	// assembled on the session output.
	splitAt := strings.LastIndex(spokenReply, " ")
	if splitAt < 0 {
		t.Fatalf("spoken reply %q has no word boundary to split at", spokenReply)
	}
	firstDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio_transcript.delta",
		"delta": spokenReply[:splitAt],
	})
	if marshalErr != nil {
		t.Fatalf("marshal first transcript delta: %v", marshalErr)
	}
	secondDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio_transcript.delta",
		"delta": spokenReply[splitAt:],
	})
	if marshalErr != nil {
		t.Fatalf("marshal second transcript delta: %v", marshalErr)
	}
	serverEvent("response.output_audio_transcript.delta", string(firstDelta))
	serverEvent("response.output_audio_transcript.delta", string(secondDelta))
	serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":`+strconvQuote(spokenReply)+`}`)

	audioDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(replySamples)),
	})
	if marshalErr != nil {
		t.Fatalf("marshal audio delta: %v", marshalErr)
	}
	serverEvent("response.output_audio.delta", string(audioDelta))
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	finalResponseID := "resp_tool_result_conversation"
	if includeToolCall {
		finalResponseID = "resp_tool_result_conversation_reply"
	}
	serverEvent("response.done", `{"type":"response.done","response":{"id":"`+finalResponseID+`","status":"completed"}}`)

	baseCapture.Session.ID = "sess_tool_result_conversation"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_tool_result_conversation","reason":"fixture_complete"}`),
	})
	wirePath := filepath.Join(t.TempDir(), "tool-result-conversation.session.json")
	wireData, err := json.MarshalIndent(baseCapture, "", "  ")
	if err != nil {
		t.Fatalf("marshal wire fixture: %v", err)
	}
	if err := os.WriteFile(wirePath, wireData, 0o600); err != nil {
		t.Fatalf("write wire fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(wirePath); err != nil {
		t.Fatalf("replay fixture rejected by the session replayer dialer: %v", err)
	}
	return wirePath
}

// runToolResultConversation drives the real 'agent session' command surface —
// wired through the production composition root with the recording executor
// swapped into the tool-executor port — over the hermetic record/replay
// transport with file-backed audio-in and --audio-out, capturing stdout so
// transcript rendering can be asserted.
func runToolResultConversation(t *testing.T, wavPath, wirePath string, executor messages.ToolExecutor) (stdout string, outputPath string, runErr error) {
	return runToolResultConversationWithBounds(t, wavPath, wirePath, executor, 8*time.Second, 10*time.Second)
}

// runToolResultConversationWithBounds is the bounded variant used by negative
// controls. A control that deliberately leaves the replay waiting for an
// impossible second result must have a short, explicit liveness bound rather
// than inheriting the positive path's larger audio-session allowance.
func runToolResultConversationWithBounds(t *testing.T, wavPath, wirePath string, executor messages.ToolExecutor, maxDuration, contextTimeout time.Duration) (stdout string, outputPath string, runErr error) {
	return runToolResultConversationWithOptions(t, wavPath, wirePath, executor, maxDuration, contextTimeout, false)
}

// runToolResultConversationWithWaitForCloseAndBounds keeps the real CLI in
// its explicit terminal-close mode. It is used by the missing-continuation
// control so the first tool-call MESSAGE.END cannot be mistaken for the final
// assistant response while the replay waits at its bounded terminal edge.
func runToolResultConversationWithWaitForCloseAndBounds(t *testing.T, wavPath, wirePath string, executor messages.ToolExecutor, maxDuration, contextTimeout time.Duration) (stdout string, outputPath string, runErr error) {
	return runToolResultConversationWithOptions(t, wavPath, wirePath, executor, maxDuration, contextTimeout, true)
}

func runToolResultConversationWithOptions(t *testing.T, wavPath, wirePath string, executor messages.ToolExecutor, maxDuration, contextTimeout time.Duration, waitForClose bool) (stdout string, outputPath string, runErr error) {
	t.Helper()
	outputPath = filepath.Join(t.TempDir(), "response.wav")
	stdoutBuffer := &testStdoutBuffer{}
	agentCLI, err := wire.InitializeMockAgentCLI(executor, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize agent CLI: %v", err)
	}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(stdoutBuffer)
	rootCmd.SetErr(io.Discard)
	args := []string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", wirePath,
		"--audio-in", wavPath,
		"--audio-out", outputPath,
		"--max-duration", maxDuration.String(),
	}
	if waitForClose {
		args = append(args, "--wait-for-close")
	}
	rootCmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
	defer cancel()
	runErr = rootCmd.ExecuteContext(ctx)
	return stdoutBuffer.String(), outputPath, runErr
}

// transcriptReflectionError is the shared spoken-reply assertion for both the
// positive path and its negative control. It fails unless the session output's
// rendered transcript quotes the values unique to the executor's actual
// runtime result. The error message names the mismatched expectation so a
// control failure is self-describing.
func transcriptReflectionError(stdout string) error {
	expectedMarkers := []string{"24 degrees", "clear skies"}
	var missing []string
	for _, marker := range expectedMarkers {
		if !strings.Contains(stdout, marker) {
			missing = append(missing, marker)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"spoken transcript does not reflect the real tool result get_weather(%s): session output is missing unique expectation values %q (executor must have returned %s for the reply to quote them)",
			toolCallScenarioArguments, missing, toolResultPositive)
	}
	return nil
}

type providerFunctionCallOutput struct {
	Sequence int
	CallID   string
	Output   string
}

// functionCallOutputsInExchange loads the authored provider exchange and
// returns every function_call_output item recorded on the client-to-server
// side. Because replay validation matches each outbound frame against these
// records, a clean completion proves the live session sent exactly these
// payloads and the provider accepted them at the wire boundary.
func functionCallOutputsInExchange(t *testing.T, wirePath string) []providerFunctionCallOutput {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange: %v", err)
	}
	var outputs []providerFunctionCallOutput
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != "conversation.item.create" {
			continue
		}
		var payload struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode provider function_call_output at sequence %d: %v", record.Sequence, err)
		}
		if payload.Item.Type == "function_call_output" {
			outputs = append(outputs, providerFunctionCallOutput{
				Sequence: record.Sequence,
				CallID:   payload.Item.CallID,
				Output:   payload.Item.Output,
			})
		}
	}
	return outputs
}

// assertToolResultFollowUpOrdering verifies the authored replay contract that
// makes the positive path causal: no follow-up transcript or audio is
// available before the provider-facing result frame. The replay connection
// releases those later server records only after its exact outbound frame
// comparison succeeds.
func assertToolResultFollowUpOrdering(t *testing.T, wirePath string, resultSequence int) {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange for ordering assertion: %v", err)
	}
	transcriptSequence := 0
	audioSequence := 0
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_audio_transcript.delta":
			if transcriptSequence == 0 {
				transcriptSequence = record.Sequence
			}
		case "response.output_audio.delta":
			if audioSequence == 0 {
				audioSequence = record.Sequence
			}
		}
	}
	if transcriptSequence == 0 || transcriptSequence <= resultSequence {
		t.Fatalf("follow-up transcript sequence = %d, want after accepted result sequence %d", transcriptSequence, resultSequence)
	}
	if audioSequence == 0 || audioSequence <= resultSequence {
		t.Fatalf("follow-up audio sequence = %d, want after accepted result sequence %d", audioSequence, resultSequence)
	}
}

// TestSessionToolCallConversationSpokenReplyReflectsRealToolResult is the full
// depth-5 positive path: voice in -> exactly one named tool call executed
// through the composed executor -> the executor's runtime return value carried
// verbatim on the provider exchange -> the spoken reply (rendered transcript
// plus audible recorded audio) quoting values unique to that result.
func TestSessionToolCallConversationSpokenReplyReflectsRealToolResult(t *testing.T) {
	wavPath := toolSingleCallWAVPath(t)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	reply := loudestWindowSamplesIntegration(t, samples, toolSingleCallReplySamples)

	executor := &conversationResultExecutor{result: toolResultPositive}
	wirePath := buildToolResultConversationFixture(t, wavPath, reply, toolResultPositive, true)
	started := time.Now()
	stdout, outputPath, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	if runErr != nil {
		t.Fatalf("agent session --replay/--audio-in/--audio-out failed: %v\nstdout:\n%s", runErr, stdout)
	}
	if elapsed := time.Since(started); elapsed > 9*time.Second {
		t.Fatalf("positive path completed after %s; the scenario must terminate deterministically well inside its bounds", elapsed)
	}

	calls, returned := executor.snapshot()
	if err := validateExactlyOneToolCall(calls); err != nil {
		t.Fatal(err)
	}
	if len(returned) != 1 || returned[0] != toolResultPositive {
		t.Fatalf("executor returned %q, want the configured runtime result %s exactly once", returned, toolResultPositive)
	}

	outputs := functionCallOutputsInExchange(t, wirePath)
	if len(outputs) != 1 {
		t.Fatalf("provider exchange contains %d function_call_output events, want exactly 1: %+v", len(outputs), outputs)
	}
	if outputs[0].CallID != toolConversationCallID {
		t.Fatalf("function_call_output call ID %q does not match originating call ID %q", outputs[0].CallID, toolConversationCallID)
	}
	if outputs[0].Output != returned[0] {
		t.Fatalf("function_call_output output %q does not carry the executor's returned content %q verbatim", outputs[0].Output, returned[0])
	}
	assertToolResultFollowUpOrdering(t, wirePath, outputs[0].Sequence)
	assertRecordedSpeech(t, outputPath, len(reply))

	if err := transcriptReflectionError(stdout); err != nil {
		t.Fatalf("spoken reply failed the reflection assertion:\n%v\nstdout-bytes=%q", err, stdout)
	}
}

// TestSessionToolCallConversationDifferentResultFailsReflection is the
// non-vacuity control: identical fixture (authored for the positive result),
// but the executor returns DIFFERENT content at runtime. The outbound
// function_call_output diverges from the fixture, the replay terminates
// deterministically before the authored reply is ever served, and the shared
// transcript-reflection assertion fails naming the mismatched expectation —
// never via timeout.
func TestSessionToolCallConversationDifferentResultFailsReflection(t *testing.T) {
	wavPath := toolSingleCallWAVPath(t)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	reply := loudestWindowSamplesIntegration(t, samples, toolSingleCallReplySamples)

	executor := &conversationResultExecutor{result: toolResultControl}
	wirePath := buildToolResultConversationFixture(t, wavPath, reply, toolResultPositive, true)

	started := time.Now()
	stdout, _, runErr := runToolResultConversation(t, wavPath, wirePath, executor)
	elapsed := time.Since(started)

	if runErr == nil {
		t.Fatalf("control run with differing executor content completed cleanly; the replay gate did not discriminate\nstdout:\n%s", stdout)
	}
	if !strings.Contains(runErr.Error(), "replay session capture") ||
		!strings.Contains(runErr.Error(), "replay mismatch") ||
		!strings.Contains(runErr.Error(), `expected event type "conversation.item.create" at sequence 101`) ||
		!strings.Contains(runErr.Error(), "JSON pointer /item/output") {
		t.Fatalf("control failure %q is not the deterministic replay divergence at the function_call_output frame", runErr)
	}
	if elapsed > 9*time.Second {
		t.Fatalf("control took %s to fail; divergence must be immediate, not a timeout", elapsed)
	}

	calls, _ := executor.snapshot()
	if err := validateExactlyOneToolCall(calls); err != nil {
		t.Fatalf("control executor still observed the named call before divergence: %v", err)
	}

	reflectionErr := transcriptReflectionError(stdout)
	if reflectionErr == nil {
		t.Fatal("shared transcript-reflection assertion passed although the executor returned different content and the authored reply was never delivered; the check does not discriminate")
	}
	for _, marker := range []string{"24 degrees", "clear skies"} {
		if !strings.Contains(reflectionErr.Error(), marker) {
			t.Fatalf("reflection-control error %q does not name mismatched expectation %q", reflectionErr, marker)
		}
	}
	if strings.Contains(stdout, "24 degrees") || strings.Contains(stdout, "clear skies") {
		t.Fatalf("authored reply leaked into session output despite divergence:\n%s", stdout)
	}
	t.Logf("negative control rejected as expected: %v", reflectionErr)
}
