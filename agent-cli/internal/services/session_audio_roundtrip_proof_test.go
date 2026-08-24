package services_test

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
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// roundtripRMSThreshold is the documented minimum RMS energy (PCM16 linear
// scale) that recorded --audio-out audio must exceed for the depth-3
// milestone proof to count as non-silent. Real speech windows in the corpus
// measure ~2000; digital silence measures 0.
const roundtripRMSThreshold = 500.0

// roundtripResponseWindowSamples is the length of the scripted spoken reply
// carried by the replay fixture: a real voiced window of the input fixture,
// so the recorded output is consistent with a genuine spoken response.
const roundtripResponseWindowSamples = 9600

// roundtripPositiveInputWAV is the real committed corpus WAV that drives the
// positive proof. Its 2.75s encoded duration fits inside the replay
// transport's bounded session window while its voiced content clears the RMS
// threshold by a wide margin (peak-window RMS ~2074).
const roundtripPositiveInputWAV = "truncated_16k.wav"

// loudestWindowSamples returns the highest-energy contiguous window of
// samples so the scripted reply mirrors genuinely voiced input content.
func loudestWindowSamples(t *testing.T, samples []int16, window int) []int16 {
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

// buildAudioRoundtripFixture writes a synthetic record/replay capture whose
// client-to-server records expect every paced frame of wavPath streamed via
// input_audio_buffer.append, followed by input_audio_buffer.commit and
// response.create, and whose server-to-client records deliver transcript
// deltas plus a terminal response.done carrying replySamples as output audio.
func buildAudioRoundtripFixture(t *testing.T, wavPath string, replySamples []int16) string {
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

	baseCapture, err := gwtesting.LoadSessionCapture(filepath.Join("..", "..", "test", "integration", "testdata", "openai_realtime_smoke.session.json"))
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
	audioDelta, marshalErr := json.Marshal(map[string]string{
		"type":  "response.output_audio.delta",
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(replySamples)),
	})
	if marshalErr != nil {
		t.Fatalf("marshal audio delta: %v", marshalErr)
	}
	serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_roundtrip"}}`)
	serverEvent("response.output_audio_transcript.delta", `{"type":"response.output_audio_transcript.delta","delta":"Hello"}`)
	serverEvent("response.output_audio_transcript.delta", `{"type":"response.output_audio_transcript.delta","delta":" there."}`)
	serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Hello there."}`)
	serverEvent("response.output_audio.delta", string(audioDelta))
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_roundtrip","status":"completed"}}`)

	baseCapture.Session.ID = "sess_audio_roundtrip_proof"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_audio_roundtrip_proof","reason":"fixture_complete"}`),
	})
	wirePath := filepath.Join(t.TempDir(), "audio-roundtrip.session.json")
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

// runAudioRoundtrip drives the real 'agent session' command surface over the
// record/replay transport with --audio-in/--audio-out and returns the path of
// the recorded output WAV.
func runAudioRoundtrip(t *testing.T, wavPath, wirePath string) (string, error) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "response.wav")
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", wirePath, "--audio-in", wavPath, "--audio-out", outputPath})
	err := cmd.ExecuteContext(context.Background())
	return outputPath, err
}

// recordedWAVSpeechViolation parses a recorded --audio-out WAV and checks
// plausible duration bounds and non-silent RMS energy. It returns a
// descriptive error with observed vs expected values, or nil when the
// recording carries speech.
func recordedWAVSpeechViolation(outputPath string, wantSamples int) error {
	wavBytes, err := os.ReadFile(outputPath)
	if err != nil {
		return fmt.Errorf("read recorded output WAV: %w", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		return fmt.Errorf("parse recorded output WAV: %w", err)
	}
	if rate != audio.SampleRate {
		return fmt.Errorf("recorded output WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	minSamples, maxSamples := wantSamples/2, wantSamples*2
	if len(samples) < minSamples || len(samples) > maxSamples {
		return fmt.Errorf("recorded duration %d samples (%.3fs) outside plausible bounds [%d, %d] derived from the scripted %.3fs reply",
			len(samples), float64(len(samples))/float64(rate), minSamples, maxSamples, float64(wantSamples)/float64(rate))
	}
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(samples)))
	if rms <= roundtripRMSThreshold {
		return fmt.Errorf("recorded output WAV RMS energy = %.1f, want > %.1f (threshold); %d samples parsed", rms, roundtripRMSThreshold, len(samples))
	}
	return nil
}

// assertRecordedWAVCarriesSpeech fails the test unless the recorded
// --audio-out WAV carries non-silent speech within plausible duration bounds.
func assertRecordedWAVCarriesSpeech(t *testing.T, outputPath string, wantSamples int) {
	t.Helper()
	if err := recordedWAVSpeechViolation(outputPath, wantSamples); err != nil {
		t.Fatal(err)
	}
}

// TestSessionCommandAudioRoundtripRecordsNonSilentReply is the depth-3
// milestone proof: the shipped 'agent session --audio-in <wav> --audio-out
// <path>' surface, driven against the hermetic record/replay transport with a
// real committed corpus WAV, must deliver byte-accurate full-stream input (the
// replay validates every outbound frame against the fixture, so any
// mid-stream truncation or byte drift fails the run) and record a spoken,
// non-silent reply whose duration stays within bounds derived from the
// fixture.
func TestSessionCommandAudioRoundtripRecordsNonSilentReply(t *testing.T) {
	wavPath := fixtureAudioWAVPath(t, roundtripPositiveInputWAV)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	reply := loudestWindowSamples(t, samples, roundtripResponseWindowSamples)

	wirePath := buildAudioRoundtripFixture(t, wavPath, reply)
	outputPath, runErr := runAudioRoundtrip(t, wavPath, wirePath)
	if runErr != nil {
		t.Fatalf("session command with --audio-in/--audio-out over replay: %v (a truncated or altered input stream diverges from the replay fixture, guarding the #156 class)", runErr)
	}
	assertRecordedWAVCarriesSpeech(t, outputPath, len(reply))
}

// TestSessionCommandAudioRoundtripSilentInputFailsAssertions is the negative
// control for the RMS assertion: a silent corpus WAV streamed through the
// identical CLI surface produces a recording whose energy the positive
// assertion rejects, proving the check discriminates silence from speech.
func TestSessionCommandAudioRoundtripSilentInputFailsAssertions(t *testing.T) {
	silencePath := fixtureAudioWAVPath(t, "silence_16k.wav")
	wavBytes, err := os.ReadFile(silencePath)
	if err != nil {
		t.Fatalf("read silent corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse silent corpus WAV: %v", err)
	}
	reply := loudestWindowSamples(t, samples, roundtripResponseWindowSamples)

	wirePath := buildAudioRoundtripFixture(t, silencePath, reply)
	outputPath, runErr := runAudioRoundtrip(t, silencePath, wirePath)
	if runErr != nil {
		t.Fatalf("silent-input session command over replay: %v", runErr)
	}

	// The exact positive-case assertion must reject the silent recording
	// with observed vs expected values.
	violation := recordedWAVSpeechViolation(outputPath, len(reply))
	if violation == nil {
		t.Fatal("positive assertions passed on a silent-input recording; the RMS check does not discriminate")
	}
	t.Logf("negative control rejected as expected: %v", violation)
}

// TestSessionCommandAudioRoundtripTruncatedInputFailsReplay is the negative
// control for the full-stream delivery assertion: feeding a committed corpus
// WAV that does not match the fixture's expected stream diverges on the very
// first non-matching frame, proving the byte-accurate transport
// validation detects input streams that do not reach the provider in full.
func TestSessionCommandAudioRoundtripTruncatedInputFailsReplay(t *testing.T) {
	wavPath := fixtureAudioWAVPath(t, roundtripPositiveInputWAV)
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	wirePath := buildAudioRoundtripFixture(t, wavPath, loudestWindowSamples(t, samples, roundtripResponseWindowSamples))

	// Feed a different committed corpus WAV against the fixture's expected
	// stream: the first non-matching frame must diverge the replay.
	mismatchedPath := fixtureAudioWAVPath(t, "silence_16k.wav")
	_, runErr := runAudioRoundtrip(t, mismatchedPath, wirePath)
	if runErr == nil {
		t.Fatal("mismatched/truncated input stream completed the replay session; the full-stream byte-accuracy guard did not discriminate")
	}
}
