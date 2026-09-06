package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// The v2b proof deliberately executes the built agent binary. The temporary
// replay capture is derived from the existing OpenAI CLI smoke fixture and
// gets its exact append payloads from the committed long corpus WAV, so the
// replay transport compares every client-to-server byte emitted by the real
// command surface.
//
// The executable itself is built once for the whole integration package by
// the package-level TestMain in s2s_v2d_multi_utterance_test.go and exposed
// as agentBinaryPath; this file only drives it as a subprocess.

type s2sV2BCLIResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func runS2SV2BAgent(t *testing.T, args ...string) s2sV2BCLIResult {
	t.Helper()

	command := exec.CommandContext(t.Context(), agentBinaryPath, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run agent %v: %v", args, err)
		}
		exitCode = exitErr.ExitCode()
	}
	return s2sV2BCLIResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func locateS2SV2BLongWAV(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve long corpus WAV path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", "utt_long_16k.wav")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("long corpus WAV missing at %q: %v", path, err)
	}
	return path
}

// s2sV2BAudioFrames mirrors the finite WAV source's frame contract: 480
// samples per 30 ms append, with a zero-padded final frame when necessary.
func s2sV2BAudioFrames(t *testing.T, wavPath string) [][]byte {
	t.Helper()

	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read long corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("decode long corpus WAV: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("long corpus WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	if len(samples) <= audio.SampleRate*10 {
		t.Fatalf("long corpus WAV has %d samples, want more than ten seconds at %d Hz", len(samples), audio.SampleRate)
	}

	frames := make([][]byte, 0, (len(samples)+audio.FrameSize-1)/audio.FrameSize)
	frame := make([]int16, audio.FrameSize)
	for start := 0; start < len(samples); start += audio.FrameSize {
		clear(frame)
		copy(frame, samples[start:])
		pcm := make([]byte, len(frame)*2)
		for i, sample := range frame {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
		}
		frames = append(frames, pcm)
	}
	if len(frames) <= 1 {
		t.Fatalf("long corpus WAV produced %d frame(s), want more than one", len(frames))
	}
	return frames
}

// s2sV2BResponsePCM16LE is a small deterministic spoken-audio stand-in for
// the replayed provider response. It is generated in memory rather than
// committed as a binary fixture, and its alternating samples make a silent
// response impossible to mistake for successful audio delivery.
func s2sV2BResponsePCM16LE() []byte {
	const sampleCount = 960
	const amplitude int16 = 1600

	pcm := make([]byte, sampleCount*2)
	for i := range sampleCount {
		sample := amplitude
		if i%2 != 0 {
			sample = -amplitude
		}
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

// buildS2SV2BLongCapture turns the existing OpenAI smoke capture into the
// exact one-turn wire trace expected from --audio-in. The client text input
// records are replaced by the WAV's append sequence; the server response and
// session-close records remain the committed smoke behavior, with a
// deterministic non-silent output-audio delta added for the --audio-out
// assertion.
func buildS2SV2BLongCapture(t *testing.T, wavPath string) (gatewaytesting.SessionCapture, int) {
	t.Helper()

	basePath := locateCLIFixture(t, "openai_realtime_smoke.session.json")
	base, err := gatewaytesting.LoadSessionCapture(basePath)
	if err != nil {
		t.Fatalf("load OpenAI smoke replay fixture: %v", err)
	}
	frames := s2sV2BAudioFrames(t, wavPath)

	var prefix []gatewaytesting.CapturedSessionEvent
	var response []gatewaytesting.CapturedSessionEvent
	var closeEvent *gatewaytesting.CapturedSessionEvent
	for _, record := range base.Records {
		switch record.Type {
		case "session.update", "session.created":
			prefix = append(prefix, record)
		case "response.created", "response.output_text.delta", "response.output_text.done", "response.done":
			response = append(response, record)
		case "session.closed":
			copyRecord := record
			closeEvent = &copyRecord
		}
	}
	if len(prefix) != 2 || len(response) == 0 || closeEvent == nil {
		t.Fatalf("OpenAI smoke fixture shape = prefix %d, response %d, close=%t; want session handshake, response, and close", len(prefix), len(response), closeEvent != nil)
	}

	capture := base
	capture.Session.ID = "sess_s2s_v2b_audio_in_long"
	capture.Session.FixtureProvenance = gatewaytesting.SessionFixtureProvenanceSynthetic
	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(prefix)+len(frames)+2+len(response)+1)
	appendRecord := func(record gatewaytesting.CapturedSessionEvent) {
		record.Sequence = len(records) + 1
		record.TimestampMs = int64(len(records))
		records = append(records, record)
	}
	for _, record := range prefix {
		appendRecord(record)
	}
	for _, frame := range frames {
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(frame),
		})
		if marshalErr != nil {
			t.Fatalf("marshal long-audio append payload: %v", marshalErr)
		}
		appendRecord(gatewaytesting.CapturedSessionEvent{
			Direction:   gatewaytesting.DirectionClientToServer,
			Type:        "input_audio_buffer.append",
			PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		})
	}
	appendRecord(gatewaytesting.CapturedSessionEvent{
		Direction:   gatewaytesting.DirectionClientToServer,
		Type:        "input_audio_buffer.commit",
		PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"input_audio_buffer.commit"}`),
	})
	appendRecord(gatewaytesting.CapturedSessionEvent{
		Direction:   gatewaytesting.DirectionClientToServer,
		Type:        "response.create",
		PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"response.create"}`),
	})
	for _, record := range response {
		appendRecord(record)
		if record.Type == "response.output_text.done" {
			audioDelta, marshalErr := json.Marshal(map[string]string{
				"type":   "response.output_audio.delta",
				"delta":  base64.StdEncoding.EncodeToString(s2sV2BResponsePCM16LE()),
				"format": "pcm16",
			})
			if marshalErr != nil {
				t.Fatalf("marshal response audio delta: %v", marshalErr)
			}
			appendRecord(gatewaytesting.CapturedSessionEvent{
				Direction:   gatewaytesting.DirectionServerToClient,
				Type:        "response.output_audio.delta",
				PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
				Payload:     audioDelta,
			})
			appendRecord(gatewaytesting.CapturedSessionEvent{
				Direction:   gatewaytesting.DirectionServerToClient,
				Type:        "response.output_audio.done",
				PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"response.output_audio.done"}`),
			})
		}
	}
	appendRecord(*closeEvent)
	capture.Records = records
	return capture, len(frames)
}

// buildS2SV2BPerChunkCommitCapture derives the negative-control capture from
// the positive one: identical records in identical order except that an
// input_audio_buffer.commit is inserted immediately after every
// input_audio_buffer.append, encoding the per-chunk-commit regression this
// lane exists to reject.
func buildS2SV2BPerChunkCommitCapture(t *testing.T, positive gatewaytesting.SessionCapture) gatewaytesting.SessionCapture {
	t.Helper()

	negative := positive
	appends := 0
	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(positive.Records)*2)
	for _, record := range positive.Records {
		clone := record
		clone.Sequence = len(records) + 1
		clone.TimestampMs = int64(len(records))
		records = append(records, clone)
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "input_audio_buffer.append" {
			appends++
			records = append(records, gatewaytesting.CapturedSessionEvent{
				Direction:   gatewaytesting.DirectionClientToServer,
				Type:        "input_audio_buffer.commit",
				PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"input_audio_buffer.commit"}`),
				Sequence:    len(records) + 1,
				TimestampMs: int64(len(records)),
			})
		}
	}
	if appends <= 1 {
		t.Fatalf("positive capture carries %d appends, want more than one before deriving the negative control", appends)
	}
	negative.Records = records
	return negative
}

// assertS2SV2BDifferOnlyByInsertedCommits pins the negative-control contract:
// the negative capture is exactly the positive trace with one
// client-to-server input_audio_buffer.commit inserted directly after every
// append. The expected layout (source index or inserted) is derived from the
// positive capture itself, so adjacent identical commit records cannot be
// mis-aligned: every mapped record must equal its positive source, and every
// unmapped slot must be a bare commit following an append.
func assertS2SV2BDifferOnlyByInsertedCommits(t *testing.T, positive, negative gatewaytesting.SessionCapture) {
	t.Helper()

	sameRecord := func(a, b gatewaytesting.CapturedSessionEvent) bool {
		return a.Direction == b.Direction && a.Type == b.Type && a.PayloadType == b.PayloadType && bytes.Equal(a.Payload, b.Payload)
	}
	isCommit := func(r gatewaytesting.CapturedSessionEvent) bool {
		return r.Direction == gatewaytesting.DirectionClientToServer && r.Type == "input_audio_buffer.commit" && string(r.Payload) == `{"type":"input_audio_buffer.commit"}`
	}
	isAppend := func(r gatewaytesting.CapturedSessionEvent) bool {
		return r.Direction == gatewaytesting.DirectionClientToServer && r.Type == "input_audio_buffer.append"
	}

	layout := make([]int, 0, len(positive.Records)*2)
	for i, record := range positive.Records {
		layout = append(layout, i)
		if isAppend(record) {
			layout = append(layout, -1)
		}
	}
	if len(layout) != len(negative.Records) {
		t.Fatalf("negative control has %d records, want %d (%d positive records plus one inserted commit per append)", len(negative.Records), len(layout), len(positive.Records))
	}

	insertedCommits := 0
	for neg, source := range layout {
		if source >= 0 {
			if !sameRecord(positive.Records[source], negative.Records[neg]) {
				t.Fatalf("negative control altered positive record %d (%s) at position %d", positive.Records[source].Sequence, positive.Records[source].Type, neg+1)
			}
			continue
		}
		followsAppend := neg > 0 && isAppend(negative.Records[neg-1])
		switch {
		case !isCommit(negative.Records[neg]):
			t.Fatalf("expected an inserted input_audio_buffer.commit at position %d, got %s/%s", neg+1, negative.Records[neg].Direction, negative.Records[neg].Type)
		case !followsAppend:
			t.Fatalf("inserted commit at negative sequence %d does not follow an append", negative.Records[neg].Sequence)
		default:
			insertedCommits++
		}
	}
	if expected := countS2SV2BEvents(positive).Appends; insertedCommits != expected {
		t.Fatalf("negative control inserted %d commits, want exactly one per append (%d)", insertedCommits, expected)
	}
}

func writeS2SV2BCapture(t *testing.T, capture gatewaytesting.SessionCapture) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "s2s_v2b_audio_in_long.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v2b replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v2b replay capture: %v", err)
	}
	return path
}

type s2sV2BEventCounts struct {
	Appends        int
	Commits        int
	ResponseCreate int
	ResponseDone   int
}

func countS2SV2BEvents(capture gatewaytesting.SessionCapture) s2sV2BEventCounts {
	var counts s2sV2BEventCounts
	for _, record := range capture.Records {
		switch {
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "input_audio_buffer.append":
			counts.Appends++
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "input_audio_buffer.commit":
			counts.Commits++
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "response.create":
			counts.ResponseCreate++
		case record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "response.done":
			counts.ResponseDone++
		}
	}
	return counts
}

// assertS2SV2BOneTurn is the load-bearing invariant of the v2b vertical: a
// long utterance streams many appends but ends the turn with exactly one
// commit, one response.create, and one completed response.
func assertS2SV2BOneTurn(capture gatewaytesting.SessionCapture) error {
	counts := countS2SV2BEvents(capture)
	if counts.Appends <= 1 || counts.Commits != 1 || counts.ResponseCreate != 1 || counts.ResponseDone != 1 {
		return fmt.Errorf("long-audio one-turn invariant: expected append>1, commit=1, response.create=1, terminal response.done=1; observed append=%d, commit=%d, response.create=%d, terminal response.done=%d", counts.Appends, counts.Commits, counts.ResponseCreate, counts.ResponseDone)
	}
	return nil
}

func TestS2SV2BAudioInLongCLIStaysOneTurn(t *testing.T) {
	t.Parallel()
	wavPath := locateS2SV2BLongWAV(t)
	capture, frameCount := buildS2SV2BLongCapture(t, wavPath)
	capturePath := writeS2SV2BCapture(t, capture)
	audioOutPath := filepath.Join(t.TempDir(), "response.wav")

	run := runS2SV2BAgent(t,
		"--config-dir", t.TempDir(),
		"session",
		"--max-duration", "30s",
		"--replay", capturePath,
		"--audio-in", wavPath,
		"--audio-out", audioOutPath,
	)
	if run.exitCode != 0 {
		t.Fatalf("agent session exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	if strings.Count(run.stdout, "OpenAI E2E replay complete.") != 1 || strings.Count(run.stdout, "[session closed: fixture_complete]") != 1 {
		t.Fatalf("agent session did not expose the replayed terminal response: stdout=%q stderr=%q", run.stdout, run.stderr)
	}

	observed, err := gatewaytesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("reload replay trace after CLI run: %v", err)
	}
	counts := countS2SV2BEvents(observed)
	if counts.Appends != frameCount {
		t.Fatalf("replay trace append count = %d, want %d frames from utt_long_16k.wav", counts.Appends, frameCount)
	}
	if err := assertS2SV2BOneTurn(observed); err != nil {
		t.Fatal(err)
	}

	audioBytes, err := os.ReadFile(audioOutPath)
	if err != nil {
		t.Fatalf("read recorded response WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(audioBytes))
	if err != nil {
		t.Fatalf("parse recorded response WAV: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("recorded response WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	if len(samples) == 0 {
		t.Fatal("recorded response WAV contains no emitted audio samples")
	}
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(samples)))
	if rms < 500 {
		t.Fatalf("recorded response RMS = %.1f, want > 500 for non-silent emitted audio", rms)
	}
}

// TestS2SV2BPerChunkCommitFixtureFailsIdenticalInvocation is the
// non-vacuousness proof: the negative control differs from the positive
// capture only by an input_audio_buffer.commit inserted after every append,
// and the identical CLI invocation must fail with diagnostics naming the
// commit-versus-append divergence. The suite passes only by asserting that
// rejection; substituting this fixture into the positive case would fail its
// assertions.
func TestS2SV2BPerChunkCommitFixtureFailsIdenticalInvocation(t *testing.T) {
	t.Parallel()
	wavPath := locateS2SV2BLongWAV(t)
	positive, _ := buildS2SV2BLongCapture(t, wavPath)
	negative := buildS2SV2BPerChunkCommitCapture(t, positive)
	assertS2SV2BDifferOnlyByInsertedCommits(t, positive, negative)
	negativeCounts := countS2SV2BEvents(negative)
	if negativeCounts.Commits != negativeCounts.Appends+1 {
		t.Fatalf("negative control carries %d commits for %d appends, want appends+1 (per-chunk commits plus the final turn commit)", negativeCounts.Commits, negativeCounts.Appends)
	}

	capturePath := writeS2SV2BCapture(t, negative)
	audioOutPath := filepath.Join(t.TempDir(), "response.wav")
	run := runS2SV2BAgent(t,
		"--config-dir", t.TempDir(),
		"session",
		"--max-duration", "30s",
		"--replay", capturePath,
		"--audio-in", wavPath,
		"--audio-out", audioOutPath,
	)
	diagnostics := run.stdout + "\n" + run.stderr
	if run.exitCode == 0 && strings.Contains(run.stdout, "[session closed: fixture_complete]") {
		t.Fatalf("per-chunk-commit fixture completed cleanly; the exactly-once proof is vacuous: stdout=%q stderr=%q", run.stdout, run.stderr)
	}

	divergence := ""
	for _, line := range strings.Split(diagnostics, "\n") {
		if strings.Contains(line, "input_audio_buffer.commit") && strings.Contains(line, "input_audio_buffer.append") {
			divergence = line
			break
		}
	}
	if divergence == "" {
		t.Fatalf("failure diagnostics do not name the divergent input_audio_buffer.commit versus input_audio_buffer.append sequence: exit=%d stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	t.Logf("per-chunk-commit negative control rejected as expected: %s", strings.TrimSpace(divergence))
}
