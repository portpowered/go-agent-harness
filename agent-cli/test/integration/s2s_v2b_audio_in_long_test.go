package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The v2b proof deliberately executes the built agent binary. The temporary
// replay capture is derived from the existing OpenAI CLI smoke fixture and
// gets its exact append payloads from the committed long corpus WAV, so the
// replay transport compares every client-to-server byte emitted by the real
// command surface.
var s2sV2BAgentBinary string

func TestMain(m *testing.M) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "resolve v2b integration test path: runtime.Caller failed")
		os.Exit(1)
	}

	buildDir, err := os.MkdirTemp("", "s2s-v2b-agent-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create v2b integration build directory: %v\n", err)
		os.Exit(1)
	}
	binaryPath := filepath.Join(buildDir, "agent")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/agent")
	build.Dir = filepath.Join(filepath.Dir(currentFile), "..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(buildDir)
		fmt.Fprintf(os.Stderr, "build agent binary for v2b integration test: %v\n", err)
		os.Exit(1)
	}
	s2sV2BAgentBinary = binaryPath

	status := m.Run()
	_ = os.RemoveAll(buildDir)
	os.Exit(status)
}

type s2sV2BCLIResult struct {
	exitCode int
	stdout   string
	stderr   string
}

func runS2SV2BAgent(t *testing.T, args ...string) s2sV2BCLIResult {
	t.Helper()

	command := exec.CommandContext(t.Context(), s2sV2BAgentBinary, args...)
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

// buildS2SV2BLongCapture turns the existing OpenAI smoke capture into the
// exact one-turn wire trace expected from --audio-in. The client text input
// records are replaced by the WAV's append sequence; the server response and
// session-close records remain the committed smoke behavior.
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
	}
	appendRecord(*closeEvent)
	capture.Records = records
	return capture, len(frames)
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

// assertS2SV2BOneTurn is the load-bearing invariant shared by the positive
// proof and the next-iteration per-chunk negative control.
func assertS2SV2BOneTurn(capture gatewaytesting.SessionCapture) error {
	counts := countS2SV2BEvents(capture)
	if counts.Appends <= 1 || counts.Commits != 1 || counts.ResponseCreate != 1 || counts.ResponseDone != 1 {
		return fmt.Errorf("long-audio one-turn invariant: expected append>1, commit=1, response.create=1, terminal response.done=1; observed append=%d, commit=%d, response.create=%d, terminal response.done=%d", counts.Appends, counts.Commits, counts.ResponseCreate, counts.ResponseDone)
	}
	return nil
}

func TestS2SV2BAudioInLongCLIStaysOneTurn(t *testing.T) {
	wavPath := locateS2SV2BLongWAV(t)
	capture, frameCount := buildS2SV2BLongCapture(t, wavPath)
	capturePath := writeS2SV2BCapture(t, capture)

	run := runS2SV2BAgent(t,
		"--config-dir", t.TempDir(),
		"session",
		"--max-duration", "30s",
		"--replay", capturePath,
		"--audio-in", wavPath,
	)
	if run.exitCode != 0 {
		t.Fatalf("agent session exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, "OpenAI E2E replay complete.") || !strings.Contains(run.stdout, "[session closed: fixture_complete]") {
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
}
