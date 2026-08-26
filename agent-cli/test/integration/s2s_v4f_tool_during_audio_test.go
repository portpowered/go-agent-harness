package integration

// s2s-v4f-tool-during-audio vertical: CLI-verified hermetic (T1) proof driving
// the real 'agent session' command over the record/replay transport with a
// spoken (file-backed audio-in) request whose replayed provider exchange
// streams one output-audio response whose deltas are split by an interleaved
// named function tool call mid-response.
//
// Proven here through the public CLI surface:
//   - every audio delta of the response survives the interleaving boundary
//     intact (count, order, byte-exact PCM16 content) in both the replayed
//     provider exchange and the recorded --audio-out artifact,
//   - the interleaved tool call (name + arguments) is observably present in
//     the replayed provider exchange strictly between the surrounding audio
//     deltas,
//   - the turn terminates cleanly with a completed response.done after the
//     final audio delta (no hang, no wedged buffer),
//   - negative controls prove the shared assertion fails deterministically
//     with a message identifying the affected delta range when the in-flight
//     audio is corrupted or dropped at the interleaving point.
//
// Like the sibling v4a single-call lane, the fixture reuses an existing
// committed corpus WAV (go-agent-loop/testdata/audio); no new binary assets
// are added.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	// toolDuringAudioToolName is the named function tool issued mid-response.
	toolDuringAudioToolName = "get_weather"
	// toolDuringAudioToolArguments is the exact argument payload carried by
	// the interleaved tool call event.
	toolDuringAudioToolArguments = `{"city":"Lisbon"}`
	// toolDuringAudioCorpusWAV is the existing committed corpus fixture
	// expressing the spoken request. Reused from go-agent-loop/testdata/audio;
	// no new audio asset is added by this lane.
	toolDuringAudioCorpusWAV = "truncated_16k.wav"
	// toolDuringAudioDeltaSamples is the PCM16 sample length of each scripted
	// output-audio delta.
	toolDuringAudioDeltaSamples = 2400
	// toolDuringAudioDeltaCount is the number of streamed output-audio deltas
	// of the single response in the clean fixture.
	toolDuringAudioDeltaCount = 6
	// toolDuringAudioInterleaveAfter is the number of audio deltas streamed
	// before the interleaved function tool call; the remaining deltas resume
	// within the same response.
	toolDuringAudioInterleaveAfter = 3
	// toolDuringAudioSessionID / toolDuringAudioResponseID identify the
	// fixture session and response.
	toolDuringAudioSessionID  = "sess_tool_during_audio"
	toolDuringAudioResponseID = "resp_tool_during_audio"
	// toolDuringAudioMaxDuration bounds the CLI run against hangs while still
	// finishing well before the bound on a healthy transport.
	toolDuringAudioMaxDuration = 5 * time.Second
)

// toolDuringAudioWAVPath resolves the committed corpus WAV reused as the
// spoken input fixture.
func toolDuringAudioWAVPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "go-agent-loop", "testdata", "audio", toolDuringAudioCorpusWAV)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed corpus WAV %s not found: %v", toolDuringAudioCorpusWAV, err)
	}
	return path
}

// toolDuringAudioCorpusSamples reads and validates the committed corpus WAV.
func toolDuringAudioCorpusSamples(t *testing.T, wavPath string) []int16 {
	t.Helper()
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("input WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	return samples
}

// toolDuringAudioPCM16LEBytes encodes samples as little-endian PCM16 bytes.
func toolDuringAudioPCM16LEBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

// toolDuringAudioLoudestWindow mirrors the corpus helpers: the highest-energy
// contiguous sample window, so scripted reply deltas carry genuinely voiced
// content rather than synthetic silence.
func toolDuringAudioLoudestWindow(t *testing.T, samples []int16, window int) []int16 {
	t.Helper()
	if len(samples) < window {
		t.Fatalf("fixture has %d samples; want at least %d", len(samples), window)
	}
	bestStart, bestEnergy := 0, -1.0
	for start := 0; start+window <= len(samples); start += audio.FrameSize {
		var energy float64
		for _, sample := range samples[start : start+window] {
			energy += float64(sample) * float64(sample)
		}
		if energy > bestEnergy {
			bestEnergy = energy
			bestStart = start
		}
	}
	out := make([]int16, window)
	copy(out, samples[bestStart:bestStart+window])
	return out
}

// toolDuringAudioScriptedDeltas derives the expected output-audio deltas from
// one voiced corpus window. Delta i carries the window shifted by i in its
// low bits, so consecutive deltas differ everywhere while keeping the spoken
// amplitude envelope; any swap or duplication between deltas is detectable.
func toolDuringAudioScriptedDeltas(t *testing.T, samples []int16) [][]int16 {
	t.Helper()
	window := toolDuringAudioLoudestWindow(t, samples, toolDuringAudioDeltaSamples)
	deltas := make([][]int16, toolDuringAudioDeltaCount)
	for i := range deltas {
		deltas[i] = make([]int16, toolDuringAudioDeltaSamples)
		for j := range window {
			deltas[i][j] = window[j] + int16(i)
		}
	}
	return deltas
}

// buildToolDuringAudioFixture writes a synthetic record/replay capture whose
// client-to-server side expects every paced frame of the spoken corpus input
// via input_audio_buffer.append followed by commit and response.create, and
// whose server-to-client side streams the pre deltas of one response,
// interleaves exactly one named function tool call mid-response, then resumes
// the post deltas and completes the response. Tampering variants pass mutated
// or shortened slices for pre/post without any other change.
func buildToolDuringAudioFixture(t *testing.T, wavPath string, pre, post [][]int16) string {
	t.Helper()
	inputSamples := toolDuringAudioCorpusSamples(t, wavPath)
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

	// One append per streamed frame; the final short frame is zero-padded by
	// the source exactly as ReadFrame documents.
	frame := make([]int16, audio.FrameSize)
	for start := 0; start < len(inputSamples); start += audio.FrameSize {
		clear(frame)
		copy(frame, inputSamples[start:])
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(toolDuringAudioPCM16LEBytes(frame)),
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

	audioDeltaPayload := func(samples []int16) string {
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "response.output_audio.delta",
			"delta": base64.StdEncoding.EncodeToString(toolDuringAudioPCM16LEBytes(samples)),
		})
		if marshalErr != nil {
			t.Fatalf("marshal audio delta: %v", marshalErr)
		}
		return string(payload)
	}

	serverEvent("response.created", `{"type":"response.created","response":{"id":"`+toolDuringAudioResponseID+`"}}`)
	serverEvent("response.output_audio_transcript.delta", `{"type":"response.output_audio_transcript.delta","delta":"Checking the weather now."}`)
	for _, samples := range pre {
		serverEvent("response.output_audio.delta", audioDeltaPayload(samples))
	}
	serverEvent("response.output_item.added",
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_weather_1","name":"`+toolDuringAudioToolName+`"}}`)
	serverEvent("response.function_call_arguments.done",
		`{"type":"response.function_call_arguments.done","call_id":"call_weather_1","name":"`+toolDuringAudioToolName+`","arguments":`+strconvQuote(toolDuringAudioToolArguments)+`}`)
	for _, samples := range post {
		serverEvent("response.output_audio.delta", audioDeltaPayload(samples))
	}
	serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Checking the weather now."}`)
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	serverEvent("response.done", `{"type":"response.done","response":{"id":"`+toolDuringAudioResponseID+`","status":"completed"}}`)

	baseCapture.Session.ID = toolDuringAudioSessionID
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"` + toolDuringAudioSessionID + `","reason":"fixture_complete"}`),
	})

	wirePath := filepath.Join(t.TempDir(), "tool-during-audio.session.json")
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

// runToolDuringAudio drives the real 'agent session' command surface over the
// hermetic record/replay transport with file-backed audio-in and audio-out.
func runToolDuringAudio(t *testing.T, wavPath, wirePath string) (string, string, error) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--replay", wirePath,
		"--audio-in", wavPath,
		"--audio-out", outputPath,
		"--max-duration", toolDuringAudioMaxDuration.String(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*toolDuringAudioMaxDuration)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return outputPath, stdout.String(), err
}

// toolDuringAudioExchange is the ordered observation of the replayed provider
// exchange relevant to the interleaving proof.
type toolDuringAudioExchange struct {
	audioDeltaIndices  []int // record indexes of streamed output-audio deltas, in order
	streamedCount      int
	streamedDeltas     [][]byte
	toolCallCount      int
	argumentsMatched   int
	toolCallIndex      int // record index of the named tool call; -1 when absent
	completedDoneSeen  bool
	completedDoneIndex int // record index of completed response.done; -1 when absent
}

// observeToolDuringAudioExchange loads the replayed provider exchange and
// records the ordered positions of output-audio deltas, the named function
// tool call, and the completed terminal response.
func observeToolDuringAudioExchange(t *testing.T, wirePath string) toolDuringAudioExchange {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(wirePath)
	if err != nil {
		t.Fatalf("load replayed provider exchange: %v", err)
	}
	exchange := toolDuringAudioExchange{toolCallIndex: -1, completedDoneIndex: -1}
	for i, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.output_audio.delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(record.Payload, &payload) != nil || payload.Delta == "" {
				t.Fatalf("record %d is not a decodable output-audio delta: %s", i, record.Payload)
			}
			pcm, decodeErr := base64.StdEncoding.DecodeString(payload.Delta)
			if decodeErr != nil {
				t.Fatalf("record %d audio delta base64: %v", i, decodeErr)
			}
			exchange.audioDeltaIndices = append(exchange.audioDeltaIndices, i)
			exchange.streamedCount++
			exchange.streamedDeltas = append(exchange.streamedDeltas, pcm)
		case "response.function_call_arguments.done":
			var payload struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(record.Payload, &payload) != nil || payload.Name != toolDuringAudioToolName {
				continue
			}
			exchange.toolCallCount++
			if payload.Arguments == toolDuringAudioToolArguments {
				exchange.argumentsMatched++
			}
			exchange.toolCallIndex = i
		case "response.done":
			var payload struct {
				Response struct {
					Status string `json:"status"`
				} `json:"response"`
			}
			if json.Unmarshal(record.Payload, &payload) != nil {
				continue
			}
			if payload.Response.Status == "completed" {
				exchange.completedDoneSeen = true
				exchange.completedDoneIndex = i
			}
		}
	}
	return exchange
}

// verifyToolDuringAudioExchangeIntact asserts the ordered exchange invariants:
// exactly one named tool call with the expected arguments, streamed deltas on
// both sides of it, byte-exact delta payloads matching expectation, and a
// completed terminal response after the final audio delta.
func verifyToolDuringAudioExchangeIntact(exchange toolDuringAudioExchange, expected [][]int16) error {
	if len(exchange.streamedDeltas) != len(expected) {
		return fmt.Errorf("replayed exchange delivered %d output-audio deltas, want %d; an interleaved turn must preserve every delta",
			len(exchange.streamedDeltas), len(expected))
	}
	if exchange.toolCallCount != 1 {
		return fmt.Errorf("replayed exchange carries %d invocations of tool %q, want exactly 1",
			exchange.toolCallCount, toolDuringAudioToolName)
	}
	if exchange.argumentsMatched != 1 {
		return fmt.Errorf("named tool call arguments mismatch, want %s", toolDuringAudioToolArguments)
	}
	preTool := 0
	for _, index := range exchange.audioDeltaIndices {
		if index < exchange.toolCallIndex {
			preTool++
		}
	}
	if preTool != toolDuringAudioInterleaveAfter || preTool >= exchange.streamedCount {
		return fmt.Errorf("replayed exchange streams %d output-audio deltas before the interleaved %q call, want %d with resuming deltas after it",
			preTool, toolDuringAudioToolName, toolDuringAudioInterleaveAfter)
	}
	if !exchange.completedDoneSeen || exchange.completedDoneIndex < exchange.toolCallIndex {
		return fmt.Errorf("replayed exchange lacks a response.done(status=completed) after the interleaved tool call; clean turn termination not observed")
	}
	lastAudioIndex := exchange.audioDeltaIndices[len(exchange.audioDeltaIndices)-1]
	if exchange.completedDoneIndex <= lastAudioIndex {
		return fmt.Errorf("replayed exchange completed before the final output-audio delta; response.done index %d, final audio index %d",
			exchange.completedDoneIndex, lastAudioIndex)
	}
	for i, delta := range expected {
		want := toolDuringAudioPCM16LEBytes(delta)
		if !bytes.Equal(exchange.streamedDeltas[i], want) {
			return fmt.Errorf("%s content mismatch in the replayed exchange (byte-exact PCM required across the interleaved tool call)",
				toolDuringAudioDeltaSpan(i))
		}
	}
	return nil
}

// toolDuringAudioDeltaSpan renders the canonical affected-range identifier
// for delta k of the scripted response.
func toolDuringAudioDeltaSpan(k int) string {
	start := k * toolDuringAudioDeltaSamples
	end := start + toolDuringAudioDeltaSamples
	return fmt.Sprintf("audio delta #%d (samples [%d,%d))", k, start, end)
}

// verifyToolDuringAudioTurnIntact proves the recorded --audio-out turn carries
// every scripted output-audio delta intact across the interleaved tool call:
// none missing, duplicated, reordered, or truncated. It returns a descriptive
// error naming the affected delta range instead of calling t.Fatal so the
// negative controls can assert failure modes deterministically.
func verifyToolDuringAudioTurnIntact(outputPath string, expected [][]int16) error {
	wantSamples := []int16{}
	for _, delta := range expected {
		wantSamples = append(wantSamples, delta...)
	}

	wavBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("read recorded --audio-out WAV: %w", err)
	}
	rate, gotSamples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		return fmt.Errorf("parse recorded --audio-out WAV: %w", err)
	}
	if rate != audio.SampleRate {
		return fmt.Errorf("recorded --audio-out WAV rate = %d, want %d", rate, audio.SampleRate)
	}

	overlap := len(gotSamples)
	if len(wantSamples) < overlap {
		overlap = len(wantSamples)
	}
	firstDivergence := -1
	for i := range overlap {
		if gotSamples[i] != wantSamples[i] {
			firstDivergence = i
			break
		}
	}
	switch {
	case len(gotSamples) == len(wantSamples):
		if firstDivergence < 0 {
			return nil
		}
		return fmt.Errorf("%s corrupted at sample offset %d: recorded %d, want %d (byte-exact PCM required across the interleaved tool call)",
			toolDuringAudioDeltaSpan(firstDivergence/toolDuringAudioDeltaSamples), firstDivergence,
			gotSamples[firstDivergence], wantSamples[firstDivergence])
	case len(gotSamples) < len(wantSamples):
		affected := firstDivergence
		if affected < 0 {
			affected = overlap // clean prefix; the loss starts at the tail
		}
		return fmt.Errorf("in-flight audio turn dropped %d of %d PCM16 samples around the interleaved tool call; first affected: %s",
			len(wantSamples)-len(gotSamples), len(wantSamples),
			toolDuringAudioDeltaSpan(affected/toolDuringAudioDeltaSamples))
	default:
		affected := firstDivergence
		if affected < 0 {
			affected = overlap // clean prefix; the excess starts at the tail
		}
		return fmt.Errorf("in-flight audio turn gained %d unexpected PCM16 samples around the interleaved tool call; first affected: %s",
			len(gotSamples)-len(wantSamples),
			toolDuringAudioDeltaSpan(affected/toolDuringAudioDeltaSamples))
	}
}

// TestSessionToolDuringAudioPreservesInFlightTurnThroughCLI is the positive
// path: the real agent session CLI streams a spoken request over the replay
// transport while the fixture interleaves one named function tool call
// between output-audio deltas of the same response. Every delta must reach
// the recorded artifact intact across the interleaving boundary, the tool
// call must appear in order in the replayed exchange, and the turn must
// terminate cleanly.
func TestSessionToolDuringAudioPreservesInFlightTurnThroughCLI(t *testing.T) {
	wavPath := toolDuringAudioWAVPath(t)
	inputSamples := toolDuringAudioCorpusSamples(t, wavPath)
	deltas := toolDuringAudioScriptedDeltas(t, inputSamples)

	wirePath := buildToolDuringAudioFixture(
		t, wavPath,
		deltas[:toolDuringAudioInterleaveAfter],
		deltas[toolDuringAudioInterleaveAfter:],
	)
	outputPath, sessionOutput, runErr := runToolDuringAudio(t, wavPath, wirePath)
	if runErr != nil {
		t.Fatalf("agent session --audio-in/--audio-out over replay failed: %v", runErr)
	}
	if !strings.Contains(sessionOutput, "[session closed: fixture_complete]") || !strings.Contains(sessionOutput, "[session terminal:") {
		t.Fatalf("agent session did not emit clean terminal output, got %q", sessionOutput)
	}

	exchange := observeToolDuringAudioExchange(t, wirePath)
	if err := verifyToolDuringAudioExchangeIntact(exchange, deltas); err != nil {
		t.Fatal(err)
	}
	if err := verifyToolDuringAudioTurnIntact(outputPath, deltas); err != nil {
		t.Fatal(err)
	}
	if len(exchange.streamedDeltas) != toolDuringAudioDeltaCount {
		t.Fatalf("replayed exchange delivered %d output-audio deltas, want %d",
			len(exchange.streamedDeltas), toolDuringAudioDeltaCount)
	}
}

// TestSessionToolDuringAudioCorruptedDeltaFailsDeterministically is the
// corruption negative control: the first resumed delta after the interleaving
// boundary carries tampered bytes, so the shared byte-exact assertion must
// fail deterministically naming that delta's range — never via timeout or
// transport error.
func TestSessionToolDuringAudioCorruptedDeltaFailsDeterministically(t *testing.T) {
	wavPath := toolDuringAudioWAVPath(t)
	inputSamples := toolDuringAudioCorpusSamples(t, wavPath)
	deltas := toolDuringAudioScriptedDeltas(t, inputSamples)

	corrupted := make([]int16, len(deltas[toolDuringAudioInterleaveAfter]))
	for j, sample := range deltas[toolDuringAudioInterleaveAfter] {
		corrupted[j] = ^sample // bitwise-not: loud, deterministic tampering
	}
	streamed := append(append([][]int16{}, deltas[:toolDuringAudioInterleaveAfter]...), corrupted)
	streamed = append(streamed, deltas[toolDuringAudioInterleaveAfter+1:]...)

	wirePath := buildToolDuringAudioFixture(t, wavPath,
		streamed[:toolDuringAudioInterleaveAfter],
		streamed[toolDuringAudioInterleaveAfter:])
	outputPath, _, runErr := runToolDuringAudio(t, wavPath, wirePath)
	if runErr != nil {
		t.Fatalf("tampered fixture must still complete the session like a healthy transport, got run error: %v", runErr)
	}

	assertionErr := verifyToolDuringAudioTurnIntact(outputPath, deltas)
	if assertionErr == nil {
		t.Fatalf("corruption of %s went undetected; the byte-exact assertion does not discriminate",
			toolDuringAudioDeltaSpan(toolDuringAudioInterleaveAfter))
	}
	wantSpan := toolDuringAudioDeltaSpan(toolDuringAudioInterleaveAfter)
	if !strings.Contains(assertionErr.Error(), wantSpan) {
		t.Fatalf("negative-control assertion error %q does not identify the affected range %q", assertionErr, wantSpan)
	}
	t.Logf("negative control rejected corruption as expected: %v", assertionErr)
}

// TestSessionToolDuringAudioDroppedDeltaFailsDeterministically is the drop
// negative control: the first resumed delta after the interleaving boundary
// never reaches the wire, so the shared assertion must fail deterministically
// naming the missing delta's range.
func TestSessionToolDuringAudioDroppedDeltaFailsDeterministically(t *testing.T) {
	wavPath := toolDuringAudioWAVPath(t)
	inputSamples := toolDuringAudioCorpusSamples(t, wavPath)
	deltas := toolDuringAudioScriptedDeltas(t, inputSamples)

	dropAt := toolDuringAudioInterleaveAfter
	streamed := append(append([][]int16{}, deltas[:dropAt]...), deltas[dropAt+1:]...)

	wirePath := buildToolDuringAudioFixture(t, wavPath,
		streamed[:toolDuringAudioInterleaveAfter],
		streamed[toolDuringAudioInterleaveAfter:])
	outputPath, _, runErr := runToolDuringAudio(t, wavPath, wirePath)
	if runErr != nil {
		t.Fatalf("dropped-delta fixture must still complete the session like a healthy transport, got run error: %v", runErr)
	}

	assertionErr := verifyToolDuringAudioTurnIntact(outputPath, deltas)
	if assertionErr == nil {
		t.Fatalf("drop of %s went undetected; the completeness assertion does not discriminate",
			toolDuringAudioDeltaSpan(dropAt))
	}
	wantSpan := toolDuringAudioDeltaSpan(dropAt)
	if !strings.Contains(assertionErr.Error(), wantSpan) {
		t.Fatalf("negative-control assertion error %q does not identify the affected range %q", assertionErr, wantSpan)
	}
	t.Logf("negative control rejected the dropped delta as expected: %v", assertionErr)
}
