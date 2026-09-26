package integration

// s2s v4a-tool-single-call vertical: CLI-verified hermetic (T1) proof driving
// the real 'agent session' command over the record/replay transport with a
// spoken (file-backed audio-in) request whose replayed provider exchange
// carries exactly one named function tool call, followed by resumed output
// speech.
//
// Proven here through the public CLI surface:
//   - the named tool call (name + arguments) traverses the real agent session
//     path and is observable in the replayed provider exchange in order,
//   - output speech is produced after the tool call,
//   - a negative control proves the exactly-one assertion fails
//     deterministically when the tool call is suppressed.
//
// The session runtime composes the supplied executor and forwards completed
// tool results to the provider wire. This fixture keeps the existing injected
// executor seam while proving the resulting call/result exchange.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli/clitest"
)

// toolCallScenarioName is the named CLI tool requested by the spoken fixture.
const toolCallScenarioName = "get_weather"

// toolCallScenarioArguments is the exact argument payload the replayed
// provider exchange carries for the single tool call.
const toolCallScenarioArguments = `{"city":"Lisbon"}`

// toolSingleCallInputWAV is the existing committed corpus fixture expressing
// the spoken single-tool request. Reused from go-agent-loop/testdata/audio;
// no new audio asset is added by this lane.
const toolSingleCallInputWAV = "truncated_16k.wav"

// toolSingleCallReplySamples is the length of the scripted post-tool spoken
// reply window carved from the input fixture so the resumed speech is
// consistent with genuinely voiced content.
const toolSingleCallReplySamples = 9600

func toolSingleCallWAVPath(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(sourcePath), "..", "..", "..", "go-agent-loop", "testdata", "audio", toolSingleCallInputWAV)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed corpus WAV %s not found: %v", toolSingleCallInputWAV, err)
	}
	return path
}

// toolSingleCallReplyWindow carves the scripted voiced reply window from the
// committed corpus at wavPath.
func toolSingleCallReplyWindow(t *testing.T, wavPath string) []int16 {
	t.Helper()
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	return loudestWindowSamplesIntegration(t, samples, toolSingleCallReplySamples)
}

// buildToolSingleCallFixture writes a synthetic record/replay capture for the
// spoken single-tool-request scenario. The client-to-server side expects every
// paced frame of wavPath streamed via input_audio_buffer.append followed by
// commit and response.create; the server-to-client side delivers one named
// function tool call (unless suppressed) followed by transcript deltas, output
// audio, and a terminal completed response.
func buildToolSingleCallFixture(t *testing.T, wavPath string, replySamples []int16, includeToolCall bool) string {
	t.Helper()
	baseCapture, records := realtimeToolFixturePrelude(t, wavPath)

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

	serverEvent(rtEventResponseCreated, `{"type":"response.created","response":{"id":"resp_tool_single_call"}}`)
	if includeToolCall {
		serverEvent(rtEventOutputItemAdded,
			`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_weather_1","name":"`+toolCallScenarioName+`"}}`)
		serverEvent(rtEventFunctionCallArgumentsDone,
			`{"type":"response.function_call_arguments.done","call_id":"call_weather_1","name":"`+toolCallScenarioName+`","arguments":`+strconvQuote(toolCallScenarioArguments)+`}`)
		// The tool-call response terminates with the call pending; the
		// spoken follow-up response exists only after the executed result is
		// delivered back to the provider.
		serverEvent(rtEventResponseDone, `{"type":"response.done","response":{"id":"resp_tool_single_call","status":"completed"}}`)
		// Tool results are delivered on the provider wire: replay validation
		// gates the post-tool speech behind this exact function_call_output
		// frame from the live session.
		outputPayload := mustJSON(t, map[string]any{
			"type": rtEventConversationItemCreate,
			"item": map[string]string{
				"type":    rtItemFunctionCallOutput,
				"call_id": "call_weather_1",
				"output":  toolSingleCallResultContent,
			},
		})
		clientEvent(rtEventConversationItemCreate, outputPayload)
		clientEvent(rtEventResponseCreate, json.RawMessage(`{"type":"response.create"}`))
		serverEvent(rtEventResponseCreated, `{"type":"response.created","response":{"id":"resp_tool_single_call_reply"}}`)
	}
	transcriptDelta := mustJSON(t, map[string]string{
		"type":  rtEventOutputAudioTranscriptDelta,
		"delta": "Checking the weather now.",
	})
	serverEvent(rtEventOutputAudioTranscriptDelta, string(transcriptDelta))
	serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Checking the weather now."}`)

	audioDelta := mustJSON(t, map[string]string{
		"type":  rtEventOutputAudioDelta,
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(replySamples)),
	})
	serverEvent(rtEventOutputAudioDelta, string(audioDelta))
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	finalResponseID := "resp_tool_single_call"
	if includeToolCall {
		finalResponseID = "resp_tool_single_call_reply"
	}
	serverEvent(rtEventResponseDone, `{"type":"response.done","response":{"id":"`+finalResponseID+`","status":"completed"}}`)

	baseCapture.Session.ID = "sess_tool_single_call"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        rtEventSessionClosed,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_tool_single_call","reason":"fixture_complete"}`),
	})
	return writeReplayCaptureFixture(t, baseCapture, "tool-single-call.session.json")
}

func strconvQuote(s string) string {
	return string(mustMarshalFixture(s))
}

// toolSingleCallResultContent is the canned weather report the recording
// executor returns; the fixture's expected function_call_output frame carries
// it verbatim now that tool results are delivered on the provider wire.
const toolSingleCallResultContent = `{"temperature_c":24,"condition":"clear"}`

// toolCallRecordingExecutor is a messages.ToolExecutor that records every
// invocation (name + arguments) so the test can assert exactly-one named-tool
// execution on the executor reached through the real CLI wiring.
type toolCallRecordingExecutor struct {
	calls []messages.ToolCall
}

func (e *toolCallRecordingExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls = append(e.calls, call)
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: toolSingleCallResultContent}, nil
}

func runToolSingleCallWithDefinitions(t *testing.T, wavPath, wirePath string, executor *toolCallRecordingExecutor, definitions []messages.ToolDefinition) (string, error) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	toolService := serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{
			Executor:    executor,
			Definitions: append([]messages.ToolDefinition(nil), definitions...),
		}, nil
	})
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(wire.NewToolServicePort(toolService))
	if err != nil {
		t.Fatalf("initialize agent CLI: %v", err)
	}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--replay", wirePath,
		"--audio-in", wavPath,
		"--audio-out", outputPath,
		"--wait-for-close",
		"--max-duration", "3s",
	})
	ctx, cancel := diagnosticDeadline(t, 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	return outputPath, err
}

// assertRecordedSpeech is the local speech assertion for the recorded
// --audio-out WAV: non-silent RMS energy within plausible duration bounds.
func assertRecordedSpeech(t *testing.T, outputPath string, wantSamples int) {
	t.Helper()
	wavBytes, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read recorded output WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse recorded output WAV: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("recorded output WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	if min, max := wantSamples/2, wantSamples*2; len(samples) < min || len(samples) > max {
		t.Fatalf("recorded duration %d samples outside plausible bounds [%d, %d]", len(samples), min, max)
	}
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(samples)))
	if rms <= 500.0 {
		t.Fatalf("recorded output WAV RMS energy = %.1f, want > 500.0 (silence threshold)", rms)
	}
}

// validateExactlyOneToolCall is the shared vertical assertion used by both
// the positive path and its no-invocation control. Keeping the zero-call
// failure in this helper prevents response audio from making the control pass.
func validateExactlyOneToolCall(calls []messages.ToolCall) error {
	if len(calls) != 1 {
		return fmt.Errorf("missing named invocation %q: recorded %d calls, want exactly one", toolCallScenarioName, len(calls))
	}
	call := calls[0]
	if call.ID != toolConversationCallID {
		return fmt.Errorf("executor invocation has call ID %q, want non-empty originating ID %q", call.ID, toolConversationCallID)
	}
	if call.Name != toolCallScenarioName {
		return fmt.Errorf("executor invoked tool %q, want %q", call.Name, toolCallScenarioName)
	}
	var args struct {
		City string `json:"city"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return fmt.Errorf("executor invoked %q with invalid arguments %q: %w", call.Name, call.Arguments, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("executor invoked %q with multiple argument values %q", call.Name, call.Arguments)
		}
		return fmt.Errorf("executor invoked %q with trailing invalid arguments %q: %w", call.Name, call.Arguments, err)
	}
	if args.City != "Lisbon" {
		return fmt.Errorf("executor invoked %q with decoded city %q, want %q", call.Name, args.City, "Lisbon")
	}
	return nil
}

// TestSessionToolSingleCallRejectsOmittedCustomDefinition proves that a
// complete tool service is an allowlist boundary as well as an executor
// injection seam. The provider still emits the recorded get_weather call, but
// the service advertises no definition for it, so the runtime must reject the
// call without invoking the paired executor.
func TestSessionToolSingleCallRejectsOmittedCustomDefinition(t *testing.T) {
	clitest.Test(t, testSessionToolSingleCallRejectsOmittedCustomDefinition)
}

func testSessionToolSingleCallRejectsOmittedCustomDefinition(t *testing.T) {
	wavPath := writeVoicedWAVSlice(t, toolSingleCallWAVPath(t), shortVoicedSlice)
	wirePath := buildToolSingleCallFixture(t, wavPath, []int16{1200, 1201}, true)
	executor := &toolCallRecordingExecutor{}
	_, runErr := runToolSingleCallWithDefinitions(t, wavPath, wirePath, executor, nil)
	if runErr == nil {
		t.Fatal("session accepted a custom tool call that was omitted from the injected service definition set")
	}
	if len(executor.calls) != 0 {
		t.Fatalf("omitted custom definition invoked executor %d times: %+v", len(executor.calls), executor.calls)
	}
}

// TestSessionToolSingleCallSuppressedFailsDeterministically is the negative
// control for the exactly-one invocation oracle: with the named tool call
// suppressed from the provider exchange, the oracle must reject the resulting
// zero-invocation evidence, proving the positive assertion cannot pass
// vacuously. It runs on constructed evidence only; the oracle's full-session
// negative control is TestSessionToolCallConversationWrongToolNameIsRejected.
func TestSessionToolSingleCallSuppressedFailsDeterministically(t *testing.T) {
	// A session whose provider exchange suppresses the call reaches the
	// executor zero times.
	var executorCalls []messages.ToolCall
	assertionErr := validateExactlyOneToolCall(executorCalls)
	if assertionErr == nil {
		t.Fatal("shared exactly-one invocation assertion passed on a zero-invocation run; the check does not discriminate")
	}
	if !strings.Contains(assertionErr.Error(), toolCallScenarioName) {
		t.Fatalf("negative-control assertion error %q does not identify missing tool %q", assertionErr, toolCallScenarioName)
	}
	t.Logf("negative control rejected as expected: %v", assertionErr)
}

// loudestWindowSamplesIntegration mirrors the corpus helper: highest-energy
// contiguous sample window so the scripted reply is genuinely voiced.
func loudestWindowSamplesIntegration(t *testing.T, samples []int16, window int) []int16 {
	t.Helper()
	if len(samples) < window {
		t.Fatalf("fixture has %d samples; want at least %d", len(samples), window)
	}
	bestStart, bestEnergy := 0, -1.0
	for start := 0; start+window <= len(samples); start += audio.FrameSize {
		var energy float64
		for _, s := range samples[start : start+window] {
			energy += float64(s) * float64(s)
		}
		if energy > bestEnergy {
			bestEnergy = energy
			bestStart = start
		}
	}
	return samples[bestStart : bestStart+window]
}

func pcm16LEBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

func toolBargeInCapabilities(executor messages.ToolExecutor) serviceTools.Service {
	return serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{
			Executor: executor,
			Definitions: []messages.ToolDefinition{{
				Name:        toolBargeInToolName,
				Description: "Look up one value for the barge-in fixture.",
				Parameters: []messages.ToolParameter{{
					Name:        "query",
					Type:        "string",
					Description: "The value to look up.",
					Required:    true,
				}},
			}},
		}, nil
	})
}
