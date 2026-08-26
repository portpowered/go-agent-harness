package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// This suite proves, through the shipped 'agent session' CLI over the
// hermetic record/replay transport, that non-speech corpus audio never
// elicits an end-of-turn commit or a transcript turn, while a real speech
// fixture driven through the identical surface produces a real commit with a
// turn. The replay transport validates every client-to-server frame against
// the fixture, so a zero-commit fixture makes any hallucinated commit fail
// the run, and a commit-requiring fixture makes a missing commit fail it.
//
// Corpus fixtures are reused from go-agent-loop/testdata/audio; no new
// fixtures are committed by this lane.

const audioInSilenceMaxDuration = "30s"

func locateCorpusWAV(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve corpus WAV path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name+".wav")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corpus WAV %q not found: %v", path, err)
	}
	return path
}

// loadCorpusHarnessSamples decodes a committed corpus WAV and returns its
// samples at the harness rate, mirroring exactly what --audio-in streams.
func loadCorpusHarnessSamples(t *testing.T, wavPath string) []int16 {
	t.Helper()
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse corpus WAV: %v", err)
	}
	if rate == audio.SampleRate {
		return samples
	}
	resampled, err := wavio.Resample(samples, rate, audio.SampleRate)
	if err != nil {
		t.Fatalf("resample corpus WAV to harness rate: %v", err)
	}
	return resampled
}

// buildZeroCommitFixture writes a synthetic replay capture whose
// client-to-server side expects every paced frame of the streamed input via
// input_audio_buffer.append and NOTHING else: no input_audio_buffer.commit
// and no response.create. The server side closes the session immediately,
// so any turn activity on either side diverges the replay and fails the run.
func buildZeroCommitFixture(t *testing.T, samples []int16) string {
	return buildAudioInWireFixture(t, samples, false)
}

// buildSpeechCommitFixture mirrors buildZeroCommitFixture but additionally
// requires the client end-of-turn signaling (input_audio_buffer.commit plus
// response.create) and delivers a spoken transcript turn in response, so the
// run can only complete when a real commit is sent.
func buildSpeechCommitFixture(t *testing.T, samples []int16) string {
	return buildAudioInWireFixture(t, samples, true)
}

func buildAudioInWireFixture(t *testing.T, samples []int16, expectTurn bool) string {
	t.Helper()

	baseCapture, err := gwtesting.LoadSessionCapture(locateCLIFixture(t, "openai_realtime_smoke.session.json"))
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

	if expectTurn {
		clientEvent("input_audio_buffer.commit", json.RawMessage(`{"type":"input_audio_buffer.commit"}`))
		clientEvent("response.create", json.RawMessage(`{"type":"response.create"}`))
		serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_silence_lane"}}`)
		serverEvent("response.output_audio_transcript.delta", `{"type":"response.output_audio_transcript.delta","delta":"Hello"}`)
		serverEvent("response.output_audio_transcript.delta", `{"type":"response.output_audio_transcript.delta","delta":" there."}`)
		serverEvent("response.output_audio_transcript.done", `{"type":"response.output_audio_transcript.done","transcript":"Hello there."}`)
		audioDelta, marshalErr := json.Marshal(map[string]string{
			"type":  "response.output_audio.delta",
			"delta": base64.StdEncoding.EncodeToString(pcm16LEBytes(replySamples(samples))),
		})
		if marshalErr != nil {
			t.Fatalf("marshal audio delta: %v", marshalErr)
		}
		serverEvent("response.output_audio.delta", string(audioDelta))
		serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
		serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_silence_lane","status":"completed"}}`)
	}

	baseCapture.Session.ID = "sess_audio_in_silence_lane"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_audio_in_silence_lane","reason":"fixture_complete"}`),
	})
	wirePath := filepath.Join(t.TempDir(), "audio-in-lane.session.json")
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

// runSessionAudioIn drives the real 'agent session' CLI command surface over
// the hermetic replay transport with --audio-in and returns stdout.
func runSessionAudioIn(t *testing.T, wavPath, wirePath, audioOutPath string) (string, error) {
	t.Helper()
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	stdout := &testStdoutBuffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(io.Discard)
	args := []string{
		"--replay", wirePath,
		"--audio-in", wavPath,
		"--max-duration", audioInSilenceMaxDuration,
	}
	if audioOutPath != "" {
		args = append(args, "--audio-out", audioOutPath)
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), err
}

type testStdoutBuffer struct {
	data []byte
}

func (b *testStdoutBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *testStdoutBuffer) String() string { return string(b.data) }

// assertNoCommitsOrTurns runs the CLI with a zero-commit fixture and asserts
// the session completed without any end-of-turn commit or transcript turn.
func assertNoCommitsOrTurns(t *testing.T, corpusName string) {
	t.Helper()
	wavPath := locateCorpusWAV(t, corpusName)
	samples := loadCorpusHarnessSamples(t, wavPath)
	wirePath := buildZeroCommitFixture(t, samples)
	out, runErr := runSessionAudioIn(t, wavPath, wirePath, "")
	if runErr != nil {
		t.Fatalf("%s through 'agent session --audio-in' diverged the zero-commit replay (a commit or turn was hallucinated): %v\nstdout:\n%s", corpusName, runErr, out)
	}
	for _, marker := range []string{"Hello", "there.", "speech detected"} {
		if containsFold(out, marker) {
			t.Fatalf("%s produced turn output containing %q; expected zero turns\nstdout:\n%s", corpusName, marker, out)
		}
	}
}

// replySamples picks a voiced window of the input so the scripted reply is
// consistent with genuinely spoken content.
func replySamples(samples []int16) []int16 {
	const window = 9600
	if len(samples) < window {
		return samples
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

func containsFold(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := range substr {
			a, b := s[i+j], substr[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestSessionAudioInSilenceFixturesProduceZeroCommitsAndTurns feeds both
// silence corpus fixtures through the agent session CLI over the hermetic
// replay transport and asserts zero commits and zero turns.
func TestSessionAudioInSilenceFixturesProduceZeroCommitsAndTurns(t *testing.T) {
	for _, name := range []string{"silence_16k", "silence_24k"} {
		t.Run(name, func(t *testing.T) {
			assertNoCommitsOrTurns(t, name)
		})
	}
}

// TestSessionAudioInNoiseFixturesProduceZeroCommitsAndTurns feeds both noise
// corpus fixtures through the identical CLI path and asserts zero commits and
// zero turns so background noise is never treated as speech.
func TestSessionAudioInNoiseFixturesProduceZeroCommitsAndTurns(t *testing.T) {
	for _, name := range []string{"noise_16k", "noise_24k"} {
		t.Run(name, func(t *testing.T) {
			assertNoCommitsOrTurns(t, name)
		})
	}
}

// TestSessionAudioInUtteranceFixtureProducesRealCommit is the negative
// control: the same CLI invocation with a real utterance fixture against a
// commit-requiring fixture must produce at least one real commit whose turn
// completes, proving the zero-commit assertions discriminate speech.
func TestSessionAudioInUtteranceFixtureProducesRealCommit(t *testing.T) {
	wavPath := locateCorpusWAV(t, "utt_short_16k")
	samples := loadCorpusHarnessSamples(t, wavPath)
	wirePath := buildSpeechCommitFixture(t, samples)
	recordedReplyPath := filepath.Join(t.TempDir(), "response.wav")
	out, runErr := runSessionAudioIn(t, wavPath, wirePath, recordedReplyPath)
	if runErr != nil {
		t.Fatalf("utterance fixture failed the commit-requiring replay (no real end-of-turn signaling was sent): %v\nstdout:\n%s", runErr, out)
	}

	// The replay fixture required input_audio_buffer.commit and
	// response.create before it would deliver the scripted turn, so reaching
	// this point proves a real commit was sent. The recorded reply must carry
	// that turn's spoken audio.
	wavBytes, err := os.ReadFile(recordedReplyPath)
	if err != nil {
		t.Fatalf("read recorded response WAV: %v", err)
	}
	_, reply, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse recorded response WAV: %v", err)
	}
	if len(reply) == 0 {
		t.Fatalf("--audio-out recorded zero samples; the committed turn delivered no spoken response\nstdout:\n%s", out)
	}
	var energy float64
	for _, sample := range reply {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(reply)))
	if rms < 500.0 {
		t.Fatalf("recorded reply RMS = %.1f, want > 500 (non-silent turn audio); %d samples", rms, len(reply))
	}
}
