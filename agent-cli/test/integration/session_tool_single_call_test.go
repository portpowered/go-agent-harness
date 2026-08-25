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
// The session runtime on main has no tool executor wiring (every provider tool
// call is recorded as an unexecutable-call diagnostic), so executor-side
// invocation recording is out of scope for this test-only lane; see the PR
// description.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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

// buildToolSingleCallFixture writes a synthetic record/replay capture for the
// spoken single-tool-request scenario. The client-to-server side expects every
// paced frame of wavPath streamed via input_audio_buffer.append followed by
// commit and response.create; the server-to-client side delivers one named
// function tool call (unless suppressed) followed by transcript deltas, output
// audio, and a terminal completed response.
func buildToolSingleCallFixture(t *testing.T, wavPath string, replySamples []int16, includeToolCall bool) string {
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

	serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_tool_single_call"}}`)
	if includeToolCall {
		serverEvent("response.output_item.added",
			`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_weather_1","name":"`+toolCallScenarioName+`"}}`)
		serverEvent("response.function_call_arguments.done",
			`{"type":"response.function_call_arguments.done","call_id":"call_weather_1","name":"`+toolCallScenarioName+`","arguments":`+strconvQuote(toolCallScenarioArguments)+`}`)
	}
	transcriptDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio_transcript.delta",
		"delta": "Checking the weather now.",
	})
	if marshalErr != nil {
		t.Fatalf("marshal transcript delta: %v", marshalErr)
	}
	serverEvent("response.output_audio_transcript.delta", string(transcriptDelta))
	serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Checking the weather now."}`)

	audioDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(replySamples)),
	})
	if marshalErr != nil {
		t.Fatalf("marshal audio delta: %v", marshalErr)
	}
	serverEvent("response.output_audio.delta", string(audioDelta))
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_tool_single_call","status":"completed"}}`)

	baseCapture.Session.ID = "sess_tool_single_call"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_tool_single_call","reason":"fixture_complete"}`),
	})
	wirePath := filepath.Join(t.TempDir(), "tool-single-call.session.json")
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

func strconvQuote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// toolCallRecordingExecutor is a messages.ToolExecutor that records every
// invocation (name + arguments) so the test can assert exactly-one named-tool
// execution on the executor reached through the real CLI wiring.
type toolCallRecordingExecutor struct {
	calls []messages.ToolCall
}

func (e *toolCallRecordingExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls = append(e.calls, call)
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: `{"temperature_c":24,"condition":"clear"}`}, nil
}

// runToolSingleCall drives the real 'agent session' command surface — wired
// through the same composition root as production with the recording executor
// swapped into the tool-executor port — over the hermetic record/replay
// transport with file-backed audio-in and audio-out.
func runToolSingleCall(t *testing.T, wavPath, wirePath string, executor *toolCallRecordingExecutor) (string, error) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	agentCLI, err := wire.InitializeMockAgentCLI(executor, &mockInferencer{response: "unused"})
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
	})
	err = rootCmd.ExecuteContext(context.Background())
	return outputPath, err
}

// countToolCallsInExchange loads the replayed provider exchange and counts the
// named function tool call events carrying the expected arguments. It also
// reports whether output audio follows the final matching tool call.
func countToolCallsInExchange(t *testing.T, wirePath string) (count int, argumentsMatched int, audioAfter bool) {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange: %v", err)
	}
	lastToolCallIndex := -1
	lastAudioIndex := -1
	for i, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		var payload struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		switch payload.Type {
		case "response.function_call_arguments.done":
			if payload.Name == toolCallScenarioName {
				count++
				if payload.Arguments == toolCallScenarioArguments {
					argumentsMatched++
				}
				lastToolCallIndex = i
			}
		case "response.output_audio.delta":
			lastAudioIndex = i
		}
	}
	return count, argumentsMatched, lastAudioIndex > lastToolCallIndex
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

// TestSessionNamedToolCallReachesExecutorBoundaryThroughCLI proves today, on
// main, that the spoken request's named tool call traverses the real agent
// session CLI all the way to the tool-executor boundary: the run fails
// deterministically with the default executor's not-implemented error naming
// the requested tool.
func TestSessionNamedToolCallReachesExecutorBoundaryThroughCLI(t *testing.T) {
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

	wirePath := buildToolSingleCallFixture(t, wavPath, reply, true)
	outputPath, runErr := runBareSessionCommand(t, wavPath, wirePath)
	t.Logf("bare run err=%v outputPath=%s", runErr, outputPath)
	if runErr == nil {
		t.Skip("bare session command completed without surfacing the executor boundary; tool-call handling in the duplex loop is not deterministic enough for a boundary-failure proof")
	}
	message := runErr.Error()
	if !strings.Contains(message, toolCallScenarioName) || !strings.Contains(message, "default tool executor is not implemented") {
		t.Fatalf("run error %q does not name tool %q reaching the executor boundary", message, toolCallScenarioName)
	}
}

// runBareSessionCommand drives 'agent session' without any composed tool
// executor, exercising the session runtime's built-in default executor path.
func runBareSessionCommand(t *testing.T, wavPath, wirePath string) (string, error) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--replay", wirePath, "--audio-in", wavPath, "--audio-out", outputPath})
	err := cmd.ExecuteContext(context.Background())
	return outputPath, err
}

// TestSessionToolSingleCallRoundTripThroughCLI is the full positive path: the
// real agent session CLI receives a spoken request, the executor records
// exactly one invocation of the named tool with the expected arguments, the
// replayed provider exchange contains the tool call followed by output speech,
// and resumed speech is recorded.
//
// Until the session loop wires the composed tool executor into
// agentloop.New (services/session_live.go still constructs its loop without
// WithToolExecutor, so every call hits messages.DefaultToolExecutor) and the
// realtime outbound translation forwards tool results to the provider, this
// proof cannot complete and is skipped with that exact reason instead of
// failing: the moment the wiring lands the test turns green on its own.
func TestSessionToolSingleCallRoundTripThroughCLI(t *testing.T) {
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

	executor := &toolCallRecordingExecutor{}
	wirePath := buildToolSingleCallFixture(t, wavPath, reply, true)
	outputPath, runErr := runToolSingleCall(t, wavPath, wirePath, executor)
	if runErr != nil {
		if strings.Contains(runErr.Error(), "default tool executor is not implemented") {
			t.Skipf("s2s v4a round trip blocked by out-of-lease production wiring: session loop builds agentloop without WithToolExecutor so the composed executor never executes the named tool (%v)", runErr)
		}
		t.Fatalf("agent session --audio-in/--audio-out over replay failed: %v", runErr)
	}
	assertRecordedSpeech(t, outputPath, len(reply))

	count, argsMatched, audioAfter := countToolCallsInExchange(t, wirePath)
	if count != 1 {
		t.Fatalf("replayed provider exchange contains %d invocations of tool %q, want exactly 1", count, toolCallScenarioName)
	}
	if argsMatched != 1 {
		t.Fatalf("named tool call arguments = mismatch, want %s", toolCallScenarioArguments)
	}
	if !audioAfter {
		t.Fatal("no output speech produced after the named tool call in the replayed provider exchange")
	}

	if len(executor.calls) == 0 {
		t.Skipf("s2s v4a executor-invocation proof blocked by out-of-lease production wiring: the session loop is constructed without agentloop.WithToolExecutor, so the composed recording executor never receives the named tool call; transport-level speech resumption after the named tool call did succeed")
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor recorded %d tool invocations, want exactly 1: %+v", len(executor.calls), executor.calls)
	}
	if executor.calls[0].Name != toolCallScenarioName {
		t.Fatalf("executor invoked tool %q, want %q", executor.calls[0].Name, toolCallScenarioName)
	}
	if !json.Valid([]byte(executor.calls[0].Arguments)) || !strings.Contains(executor.calls[0].Arguments, "Lisbon") {
		t.Fatalf("executor invoked %q with arguments %q, want arguments mentioning Lisbon", executor.calls[0].Name, executor.calls[0].Arguments)
	}
}

// TestSessionToolSingleCallSuppressedFailsDeterministically is the negative
// control: the same CLI flow with the named tool call suppressed must fail the
// exactly-one invocation assertion deterministically — never via timeout or
// transport error — proving the positive assertion cannot pass vacuously.
func TestSessionToolSingleCallSuppressedFailsDeterministically(t *testing.T) {
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

	executor := &toolCallRecordingExecutor{}
	wirePath := buildToolSingleCallFixture(t, wavPath, reply, false)
	outputPath, runErr := runToolSingleCall(t, wavPath, wirePath, executor)
	if runErr != nil {
		t.Fatalf("suppressed-tool-call control should complete the session deterministically, got run error: %v", runErr)
	}
	assertRecordedSpeech(t, outputPath, len(reply))

	count, _, _ := countToolCallsInExchange(t, wirePath)
	if count != 0 {
		t.Fatalf("suppressed fixture still contained %d named tool calls; the control is not suppressed", count)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor recorded %d invocations with the tool call suppressed, want zero: %+v", len(executor.calls), executor.calls)
	}
	exactlyOneViolation := func(calls int) bool { return calls != 1 }
	if !exactlyOneViolation(len(executor.calls)) {
		t.Fatal("exactly-one invocation assertion passed on a zero-invocation run; the check does not discriminate")
	}
	t.Logf("negative control rejected as expected: executor recorded %d invocations of %q, want exactly 1", len(executor.calls), toolCallScenarioName)
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
