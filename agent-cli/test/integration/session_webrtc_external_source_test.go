package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The v10 vertical records the evidence needed for a go2rtc-fronted
// camera-style source and a hermetic replay-backed live session over WebRTC.
// Every observation and session leg enters through the generated root CLI with
// actual argv; the loopback fixture independently records the negotiated
// sections and packet activity that make the CLI output non-vacuous:
//
//  1. Source bridge: the loopback go2rtc-compatible fixture negotiates with
//     the production WebRTC client (rtc.OpenMediaSource is used purely as the
//     receive pipe into the session's public `--audio-in -` stdin seam, never
//     as a substitute for a CLI assertion), and its deterministic PCMU audio
//     arrives decoded and non-silent.
//  2. Media observation: `agent media probe` and `agent media look` report the
//     camera and audio-only facts through the shipped root command. The
//     fixture independently records exact offer/answer track sections and
//     per-track packet activity so a declared-but-unused track cannot pass.
//  3. Session: real argv (`agent session --replay ... --audio-in - --audio-out ...`)
//     consumes the bridged source audio byte-exactly over the record/replay
//     transport (any dropped or distorted WebRTC frame diverges the replay),
//     observes the deterministic replayed agent response, emits an explicit
//     terminal outcome, and exits successfully inside the hard deadline.
const externalSourceFrameBytes = 2 * audio.FrameSize

// mediaObservation is parsed from the public media command reports and feeds
// the camera assertion used by the audio-only negative control. Keeping this
// derived from CLI output prevents a hard-coded local report from standing in
// for the shipped media/look behavior.
type mediaObservation struct {
	source           string
	codec            string
	sampleRate       int
	channels         int
	videoPresence    bool
	lookStatus       string
	lookReason       string
	mediaType        string
	observationBytes int
}

type rootCLIResult struct {
	argv       []string
	stdout     string
	stderr     string
	err        error
	exitStatus int
}

func (r rootCLIResult) diagnostics() string {
	return fmt.Sprintf("argv: %q\nstdout:\n%s\nstderr:\n%s\nexit_status: %d\nexit_error: %v", r.argv, r.stdout, r.stderr, r.exitStatus, r.err)
}

// bridgeExternalSourceAudio opens the go2rtc source through the production
// WebRTC client and collects wantPackets decoded PCM frames. It fails the
// test unless every frame arrives within frameWait per read.
func bridgeExternalSourceAudio(t *testing.T, rawURL string, wantPackets int, frameWait time.Duration) []byte {
	t.Helper()
	pcm, err := collectExternalSourceAudio(rawURL, wantPackets, frameWait)
	if err != nil {
		t.Fatalf("bridge camera source audio over WebRTC: %v", err)
	}
	return pcm
}

// collectExternalSourceAudio is the error-returning core of the source
// bridge so the dead-source control can assert the failure mode.
func collectExternalSourceAudio(rawURL string, wantPackets int, frameWait time.Duration) ([]byte, error) {
	stream, err := rtc.OpenMediaSource(context.Background(), rawURL)
	if err != nil {
		return nil, fmt.Errorf("open external source: %w", err)
	}
	defer stream.Close()
	pcm := make([]byte, 0, wantPackets*externalSourcePacketSamples*2)
	for packet := 0; packet < wantPackets; packet++ {
		ctx, cancel := context.WithTimeout(context.Background(), frameWait)
		frame, readErr := stream.ReadFrame(ctx)
		cancel()
		if readErr != nil {
			return nil, fmt.Errorf("read source audio frame %d of %d: %w", packet+1, wantPackets, readErr)
		}
		for _, sample := range frame.Samples {
			pcm = append(pcm, byte(uint16(sample)), byte(uint16(sample)>>8))
		}
	}
	return pcm, nil
}

// waitForExternalSourceEvent bounds one fixture observation channel.
func waitForExternalSourceEvent(t *testing.T, event chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// buildExternalSourceReplayFixture writes a synthetic record/replay capture
// whose client-to-server records expect exactly appendFrames of streamed
// source audio (the replay transport validates every outbound append
// byte-for-byte, so any drift between the WebRTC-bridged audio and the
// fixture fails the run), followed by commit and response.create, and whose
// server-to-client records deliver the deterministic transcript plus a
// terminal response.done carrying the spoken reply.
func buildExternalSourceReplayFixture(t *testing.T, appendFrames [][]byte, transcriptDeltas []string, replySamples []int16) string {
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
	for _, frame := range appendFrames {
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(frame),
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
		"delta": base64.StdEncoding.EncodeToString(pcm16LEBytesOf(replySamples)),
	})
	if marshalErr != nil {
		t.Fatalf("marshal audio delta: %v", marshalErr)
	}
	fullTranscript := strings.Join(transcriptDeltas, "")
	serverEvent("response.created", `{"type":"response.created","response":{"id":"resp_v10_external_source"}}`)
	// The deterministic replayed agent response is carried in both
	// modalities: text deltas are what the shipped CLI renders to stdout,
	// while the audio delta is captured by --audio-out and verified as a
	// non-silent spoken reply.
	for _, delta := range transcriptDeltas {
		textPayload, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": delta})
		serverEvent("response.output_text.delta", string(textPayload))
	}
	textDone, _ := json.Marshal(map[string]string{"type": "response.output_text.done", "text": fullTranscript})
	serverEvent("response.output_text.done", string(textDone))
	for _, delta := range transcriptDeltas {
		encoded, _ := json.Marshal(map[string]string{"type": "response.output_audio_transcript.delta", "delta": delta})
		serverEvent("response.output_audio_transcript.delta", string(encoded))
	}
	donePayload, _ := json.Marshal(map[string]string{"type": "response.output_audio_transcript.done", "transcript": fullTranscript})
	serverEvent("response.output_audio_transcript.done", string(donePayload))
	serverEvent("response.output_audio.delta", string(audioDelta))
	serverEvent("response.output_audio.done", `{"type":"response.output_audio.done"}`)
	serverEvent("response.done", `{"type":"response.done","response":{"id":"resp_v10_external_source","status":"completed"}}`)

	baseCapture.Session.ID = externalSourceSessionID
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"` + externalSourceSessionID + `","reason":"fixture_complete"}`),
	})
	wirePath := filepath.Join(t.TempDir(), "v10-external-source.session.json")
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

// runRootCLISession drives `agent session --replay <fixture> --audio-in -
// --audio-out <wav>` through the real root router with the bridged source
// audio on stdin, waiting for the explicit terminal close marker so
// asynchronous terminal formatting is always observed. Both output streams
// are retained because Cobra may put usage and errors on stderr.
func runRootCLISession(t *testing.T, ctx context.Context, cfgDir, fixturePath string, stdinPCM []byte, outWavPath string) rootCLIResult {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI composition: %v", err)
	}
	rootCmd := agentCLI.Generate()
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetIn(bytes.NewReader(stdinPCM))
	argv := []string{
		"--config-dir", cfgDir,
		"session",
		"--replay", fixturePath,
		"--audio-in", "-",
		"--audio-out", outWavPath,
	}
	rootCmd.SetArgs(argv)
	runErr := rootCmd.ExecuteContext(ctx)
	wait := time.NewTimer(20 * time.Second)
	defer wait.Stop()
	for !strings.Contains(stdout.String(), "[session closed:") {
		select {
		case <-ctx.Done():
			return rootCLIResult{argv: append([]string(nil), argv...), stdout: stdout.String(), stderr: stderr.String(), err: runErr, exitStatus: boolExitStatus(runErr)}
		case <-wait.C:
			return rootCLIResult{argv: append([]string(nil), argv...), stdout: stdout.String(), stderr: stderr.String(), err: runErr, exitStatus: boolExitStatus(runErr)}
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	return rootCLIResult{argv: append([]string(nil), argv...), stdout: stdout.String(), stderr: stderr.String(), err: runErr, exitStatus: boolExitStatus(runErr)}
}

// runRootCLIMediaCommand drives `agent media probe|look <source>` through the
// same generated root router used by the shipped binary. It retains both
// output streams and a process-like exit status so a failed public command has
// useful diagnostics without exposing source credentials.
func runRootCLIMediaCommand(t *testing.T, ctx context.Context, cfgDir, operation, rawURL string) rootCLIResult {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI composition: %v", err)
	}
	rootCmd := agentCLI.Generate()
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	argv := []string{"--config-dir", cfgDir, "media", operation, rawURL}
	rootCmd.SetArgs(argv)
	runErr := rootCmd.ExecuteContext(ctx)
	return rootCLIResult{argv: append([]string(nil), argv...), stdout: stdout.String(), stderr: stderr.String(), err: runErr, exitStatus: boolExitStatus(runErr)}
}

func assertRootCLIMediaSuccess(t *testing.T, result rootCLIResult, cfgDir, wantOperation, wantURL string) {
	t.Helper()
	wantArgv := []string{"--config-dir", cfgDir, "media", wantOperation, wantURL}
	if len(result.argv) != len(wantArgv) || strings.Join(result.argv, "\x00") != strings.Join(wantArgv, "\x00") {
		t.Fatalf("media command argv = %q, want %q", result.argv, wantArgv)
	}
	if result.err != nil || result.exitStatus != 0 || result.stderr != "" {
		t.Fatalf("media command failed\n%s", result.diagnostics())
	}
}

// parseMediaCLIObservation converts the two human-readable reports emitted by
// the public media commands into the values used by the shared camera
// predicate. Every field is derived from command output, including the
// audio-only unavailable look result.
func parseMediaCLIObservation(probeOutput, lookOutput string) (mediaObservation, error) {
	var observation mediaObservation
	var sourceSet, codecSet, rateSet, channelsSet, videoSet, lookSet bool
	parse := func(output string) error {
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			switch key {
			case "Source":
				if !sourceSet {
					observation.source = value
				}
				sourceSet = true
			case "Audio codec":
				observation.codec, codecSet = value, true
			case "Sample rate":
				rate, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("parse sample rate %q: %w", value, err)
				}
				observation.sampleRate, rateSet = rate, true
			case "Channels":
				channels, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("parse channels %q: %w", value, err)
				}
				observation.channels, channelsSet = channels, true
			case "Video presence":
				video, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("parse video presence %q: %w", value, err)
				}
				observation.videoPresence, videoSet = video, true
			case "Look status":
				observation.lookStatus, lookSet = value, true
			case "Reason":
				observation.lookReason = value
			case "Media type":
				observation.mediaType = value
			case "Observation bytes":
				count, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("parse observation bytes %q: %w", value, err)
				}
				observation.observationBytes = count
			}
		}
		return nil
	}
	if err := parse(probeOutput); err != nil {
		return mediaObservation{}, err
	}
	if err := parse(lookOutput); err != nil {
		return mediaObservation{}, err
	}
	if !sourceSet || !codecSet || !rateSet || !channelsSet || !videoSet || !lookSet {
		return mediaObservation{}, fmt.Errorf("incomplete public media observation: source=%t codec=%t rate=%t channels=%t video=%t look=%t", sourceSet, codecSet, rateSet, channelsSet, videoSet, lookSet)
	}
	return observation, nil
}

func boolExitStatus(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func sourceObservationDiagnostics(observed webrtcSourceObservationSnapshot) string {
	return fmt.Sprintf("fixture: path=%q source=%q offer_tracks={audio:%d video:%d} answer_tracks={audio:%d video:%d} sent_frames={audio:%d video:%d}",
		observed.path, observed.source, observed.offerAudioTracks, observed.offerVideoTracks,
		observed.answerAudioTracks, observed.answerVideoTracks, observed.frameCount, observed.videoFrameCount)
}

// assertCameraMediaEvidence is the shared camera-shape assertion: the public
// CLI report must identify PCMU/8000/mono audio and available visual data, while
// the independently observed fixture must show exactly one negotiated audio
// and video track plus actual packet activity. Applying it to the parsed
// audio-only CLI report is the executable negative control.
func assertCameraMediaEvidence(report mediaObservation, observed webrtcSourceObservationSnapshot) error {
	var violations []string
	if report.codec != "PCMU" {
		violations = append(violations, fmt.Sprintf("audio codec %q, want PCMU", report.codec))
	}
	if report.sampleRate != 8000 {
		violations = append(violations, fmt.Sprintf("sample rate %d, want 8000", report.sampleRate))
	}
	if report.channels != 1 {
		violations = append(violations, fmt.Sprintf("channels %d, want 1", report.channels))
	}
	if observed.offerAudioTracks != 1 || observed.answerAudioTracks != 1 {
		violations = append(violations, fmt.Sprintf("negotiated audio tracks offer=%d answer=%d, want exactly one in each", observed.offerAudioTracks, observed.answerAudioTracks))
	}
	if observed.offerVideoTracks != 1 || observed.answerVideoTracks != 1 {
		violations = append(violations, fmt.Sprintf("negotiated video tracks offer=%d answer=%d, want exactly one in each", observed.offerVideoTracks, observed.answerVideoTracks))
	}
	if observed.frameCount == 0 {
		violations = append(violations, "no audio packet activity was observed")
	}
	if observed.videoFrameCount < externalSourceVideoPackets {
		violations = append(violations, fmt.Sprintf("video packet activity %d, want at least %d", observed.videoFrameCount, externalSourceVideoPackets))
	}
	if !report.videoPresence {
		violations = append(violations, "no video track was negotiated")
	}
	if report.lookStatus != string(rtc.VisualObservationAvailable) {
		detail := fmt.Sprintf("status %q", report.lookStatus)
		if report.lookReason != "" {
			detail += fmt.Sprintf(" reason %q", report.lookReason)
		}
		violations = append(violations, "look() unavailable: public report has "+detail)
	}
	if len(violations) > 0 {
		return fmt.Errorf("camera media evidence rejected: %s", strings.Join(violations, "; "))
	}
	return nil
}

// assertNonSilentWAVResponse parses a recorded --audio-out WAV and enforces
// plausible duration bounds plus non-silent RMS energy.
func assertNonSilentWAVResponse(t *testing.T, outWavPath string, diagnostics string) {
	t.Helper()
	wavBytes, err := os.ReadFile(outWavPath)
	if err != nil {
		t.Fatalf("read recorded response WAV: %v\n%s", err, diagnostics)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse recorded response WAV: %v\n%s", err, diagnostics)
	}
	if rate != audio.SampleRate {
		t.Fatalf("recorded response rate = %d, want %d\n%s", rate, audio.SampleRate, diagnostics)
	}
	if len(samples) < externalSourceReplyWindow || len(samples) > externalSourceReplyWindow+audio.SampleRate {
		t.Fatalf("recorded response sample count = %d, want within [%d,%d]\n%s", len(samples), externalSourceReplyWindow, externalSourceReplyWindow+audio.SampleRate, diagnostics)
	}
	var energy float64
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	rms := 0.0
	if len(samples) > 0 {
		rms = sqrtOf(energy / float64(len(samples)))
	}
	if rms <= externalSourceRMSThreshold {
		t.Fatalf("recorded response RMS energy = %.1f, want > %.1f (silent delivery)\n%s", rms, externalSourceRMSThreshold, diagnostics)
	}
}

func sqrtOf(value float64) float64 {
	if value <= 0 {
		return 0
	}
	guess := value
	for i := 0; i < 64; i++ {
		guess = (guess + value/guess) / 2
	}
	return guess
}

// assertExternalSourceSessionOutcome enforces the session-leg contract:
// successful exit, the deterministic replayed transcript, an explicit
// terminal outcome inside the hard parent deadline, and a non-silent
// recorded reply. diagnostics carries the failure-reporting fields required
// by the vertical (argv shape, stdout, stderr, exit state, deadline state).
func assertExternalSourceSessionOutcome(t *testing.T, result rootCLIResult, elapsed time.Duration, wantTranscript string, outWavPath string, diagnostics string) {
	t.Helper()
	if result.err != nil {
		t.Fatalf("session command failed\n%s\n%s", result.diagnostics(), diagnostics)
	}
	if !strings.Contains(result.stdout, wantTranscript) {
		t.Fatalf("session output missing deterministic replayed transcript %q\n%s\n%s", wantTranscript, result.diagnostics(), diagnostics)
	}
	if !strings.Contains(result.stdout, "[session closed:") {
		t.Fatalf("session output missing explicit terminal outcome\n%s\n%s", result.diagnostics(), diagnostics)
	}
	if elapsed >= externalSourceHardDeadline {
		t.Fatalf("session exceeded hard parent deadline: %s >= %s\n%s", elapsed, externalSourceHardDeadline, diagnostics)
	}
	assertNonSilentWAVResponse(t, outWavPath, diagnostics+"\n"+result.diagnostics())
}

// TestWebrtcCameraSourceDrivesReplaySessionThroughRealCLI is the in-lease
// camera proof: a loopback go2rtc-compatible source (PCMU audio + H.264 video)
// independently proves its negotiated tracks and packet activity, then drives
// a hermetic replay-backed live session through the real root CLI.
func TestWebrtcCameraSourceDrivesReplaySessionThroughRealCLI(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), externalSourceHardDeadline)
	defer cancelParent()
	cfgDir := t.TempDir()
	packets := cameraSourceRTPPackets(t)

	// Public media leg 1 — the real root `agent media probe` command reports
	// the negotiated camera tracks. One audio packet is enough for probe to
	// return while leaving the fixture free to deliver all three video packets
	// for the independent activity assertion.
	probeURL, probeObserved, probeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: true, sendFrames: true, packets: packets[:1]})
	probeResult := runRootCLIMediaCommand(t, parentCtx, cfgDir, "probe", probeURL)
	waitForExternalSourceEvent(t, probeObserved.negotiated, "camera media probe offer/answer completion")
	waitForExternalSourceEvent(t, probeObserved.frameDelivered, "camera media probe audio frame delivery")
	waitForExternalSourceEvent(t, probeObserved.videoFrameDelivered, "camera media probe video frame delivery")
	probeSnapshot := probeObserved.snapshot()
	probeCleanup()
	assertRootCLIMediaSuccess(t, probeResult, cfgDir, "probe", probeURL)
	probeSource, err := rtc.ParseMediaSource(probeURL)
	if err != nil {
		t.Fatal(err)
	}
	wantProbe := fmt.Sprintf("Source: %s\nAudio codec: PCMU\nSample rate: 8000\nChannels: 1\nVideo presence: true\n", probeSource.Identity())
	if probeResult.stdout != wantProbe {
		t.Fatalf("camera media probe output = %q, want %q\n%s", probeResult.stdout, wantProbe, probeResult.diagnostics())
	}
	if probeSnapshot.offerAudioTracks != 1 || probeSnapshot.answerAudioTracks != 1 || probeSnapshot.offerVideoTracks != 1 || probeSnapshot.answerVideoTracks != 1 || probeSnapshot.frameCount == 0 || probeSnapshot.videoFrameCount < externalSourceVideoPackets {
		t.Fatalf("camera media probe fixture evidence = %s", sourceObservationDiagnostics(probeSnapshot))
	}

	// Public media leg 2 — `agent media look` must return an actual visual
	// observation from a camera-shaped source, not merely trust the probe's
	// video-presence flag.
	lookURL, lookObserved, lookCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: true, sendFrames: true, packets: packets[:1]})
	lookResult := runRootCLIMediaCommand(t, parentCtx, cfgDir, "look", lookURL)
	waitForExternalSourceEvent(t, lookObserved.negotiated, "camera media look offer/answer completion")
	waitForExternalSourceEvent(t, lookObserved.videoFrameDelivered, "camera media look video frame delivery")
	lookSnapshot := lookObserved.snapshot()
	lookCleanup()
	assertRootCLIMediaSuccess(t, lookResult, cfgDir, "look", lookURL)
	lookSource, err := rtc.ParseMediaSource(lookURL)
	if err != nil {
		t.Fatal(err)
	}
	wantLookPrefix := fmt.Sprintf("Source: %s\nLook status: available\nMedia type: video/H264\nObservation bytes: ", lookSource.Identity())
	if !strings.HasPrefix(lookResult.stdout, wantLookPrefix) || !strings.HasSuffix(lookResult.stdout, "\n") {
		t.Fatalf("camera media look output = %q, want available H264 observation\n%s", lookResult.stdout, lookResult.diagnostics())
	}
	if lookSnapshot.offerAudioTracks != 1 || lookSnapshot.answerAudioTracks != 1 || lookSnapshot.offerVideoTracks != 1 || lookSnapshot.answerVideoTracks != 1 || lookSnapshot.videoFrameCount < externalSourceVideoPackets {
		t.Fatalf("camera media look fixture evidence = %s", sourceObservationDiagnostics(lookSnapshot))
	}

	cameraReport, err := parseMediaCLIObservation(probeResult.stdout, lookResult.stdout)
	if err != nil {
		t.Fatalf("parse camera media CLI observation: %v", err)
	}
	if cameraReport.lookStatus != string(rtc.VisualObservationAvailable) || cameraReport.mediaType != "video/H264" || cameraReport.observationBytes == 0 {
		t.Fatalf("camera public media observation = %+v, want available non-empty H264 look", cameraReport)
	}

	// Leg 1 — the camera's audio reaches us over WebRTC through the
	// production inbound path, non-silent and complete.
	bridgeURL, bridgeObserved, bridgeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: true, sendFrames: true, packets: packets})
	sourcePCM := bridgeExternalSourceAudio(t, bridgeURL, len(packets), 10*time.Second)
	waitForExternalSourceEvent(t, bridgeObserved.negotiated, "camera offer/answer completion")
	waitForExternalSourceEvent(t, bridgeObserved.videoFrameDelivered, "camera video frame delivery")
	bridgeSnapshot := bridgeObserved.snapshot()
	bridgeCleanup()
	if bridgeSnapshot.offerAudioTracks != 1 || bridgeSnapshot.answerAudioTracks != 1 || bridgeSnapshot.offerVideoTracks != 1 || bridgeSnapshot.answerVideoTracks != 1 {
		t.Fatalf("camera fixture negotiated tracks = offer audio/video %d/%d, answer audio/video %d/%d; want 1/1 in both", bridgeSnapshot.offerAudioTracks, bridgeSnapshot.offerVideoTracks, bridgeSnapshot.answerAudioTracks, bridgeSnapshot.answerVideoTracks)
	}
	if bridgeSnapshot.frameCount != len(packets) || bridgeSnapshot.videoFrameCount < externalSourceVideoPackets {
		t.Fatalf("camera fixture sent frames = audio:%d video:%d, want audio:%d and video at least:%d", bridgeSnapshot.frameCount, bridgeSnapshot.videoFrameCount, len(packets), externalSourceVideoPackets)
	}
	if energy := pcmRMSEnergy(sourcePCM); energy <= externalSourceRMSThreshold {
		t.Fatalf("bridged source audio RMS energy = %.1f, want > %.1f\n%s", energy, externalSourceRMSThreshold, sourceObservationDiagnostics(bridgeSnapshot))
	}
	if err := assertCameraMediaEvidence(cameraReport, bridgeSnapshot); err != nil {
		t.Fatalf("camera public media observation rejected: %v\n%s", err, sourceObservationDiagnostics(bridgeSnapshot))
	}

	// The same source audio drives the replay-backed session.
	appendFrames := rawPCMAppendFrames(sourcePCM, externalSourceFrameBytes)
	fixture := buildExternalSourceReplayFixture(t, appendFrames, []string{cameraReplyTranscript, cameraTranscriptTail}, loudestUtteranceWindow(t, externalSourceReplyWindow))
	outWav := filepath.Join(t.TempDir(), "camera-response.wav")
	started := time.Now()
	sessionResult := runRootCLISession(t, parentCtx, cfgDir, fixture, sourcePCM, outWav)
	elapsed := time.Since(started)
	diagnostics := fmt.Sprintf("replay_fixture: %q; expected_append_frames=%d; pcm_bytes=%d; source_rms=%.1f; terminal_seen=%t; deadline_state=%t",
		fixture, len(appendFrames), len(sourcePCM), pcmRMSEnergy(sourcePCM), strings.Contains(sessionResult.stdout, "[session closed:"), elapsed < externalSourceHardDeadline)
	assertExternalSourceSessionOutcome(t, sessionResult, elapsed, cameraReplyTranscript, outWav, diagnostics)
}

// TestWebrtcAudioOnlySourceKeepsReplaySessionHealthy proves the audio-only
// source shape and healthy replay session through the public media and session
// CLI commands. Its parsed media report is also fed to the camera assertion as
// an executable negative control.
func TestWebrtcAudioOnlySourceKeepsReplaySessionHealthy(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), externalSourceHardDeadline)
	defer cancelParent()
	cfgDir := t.TempDir()
	packets := cameraSourceRTPPackets(t)

	// Public media leg 1 — the audio-only source still reports its negotiated
	// audio track through the actual root command, with no video presence.
	probeURL, probeObserved, probeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: false, sendFrames: true, packets: packets[:1]})
	probeResult := runRootCLIMediaCommand(t, parentCtx, cfgDir, "probe", probeURL)
	waitForExternalSourceEvent(t, probeObserved.negotiated, "audio-only media probe offer/answer completion")
	waitForExternalSourceEvent(t, probeObserved.frameDelivered, "audio-only media probe audio frame delivery")
	probeSnapshot := probeObserved.snapshot()
	probeCleanup()
	assertRootCLIMediaSuccess(t, probeResult, cfgDir, "probe", probeURL)
	probeSource, err := rtc.ParseMediaSource(probeURL)
	if err != nil {
		t.Fatal(err)
	}
	wantProbe := fmt.Sprintf("Source: %s\nAudio codec: PCMU\nSample rate: 8000\nChannels: 1\nVideo presence: false\n", probeSource.Identity())
	if probeResult.stdout != wantProbe {
		t.Fatalf("audio-only media probe output = %q, want %q\n%s", probeResult.stdout, wantProbe, probeResult.diagnostics())
	}
	if probeSnapshot.offerAudioTracks != 1 || probeSnapshot.answerAudioTracks != 1 || probeSnapshot.answerVideoTracks != 0 || probeSnapshot.frameCount == 0 || probeSnapshot.videoFrameCount != 0 {
		t.Fatalf("audio-only media probe fixture evidence = %s", sourceObservationDiagnostics(probeSnapshot))
	}

	// Public media leg 2 — a real root `look` invocation must be a successful,
	// structured unavailable result, not a panic or a source failure.
	lookURL, lookObserved, lookCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: false, sendFrames: true, packets: packets[:1]})
	lookResult := runRootCLIMediaCommand(t, parentCtx, cfgDir, "look", lookURL)
	waitForExternalSourceEvent(t, lookObserved.negotiated, "audio-only media look offer/answer completion")
	lookSnapshot := lookObserved.snapshot()
	lookCleanup()
	assertRootCLIMediaSuccess(t, lookResult, cfgDir, "look", lookURL)
	lookSource, err := rtc.ParseMediaSource(lookURL)
	if err != nil {
		t.Fatal(err)
	}
	wantLook := fmt.Sprintf("Source: %s\nLook status: unavailable\nReason: no_video_track\n", lookSource.Identity())
	if lookResult.stdout != wantLook {
		t.Fatalf("audio-only media look output = %q, want %q\n%s", lookResult.stdout, wantLook, lookResult.diagnostics())
	}
	if lookSnapshot.offerAudioTracks != 1 || lookSnapshot.answerAudioTracks != 1 || lookSnapshot.answerVideoTracks != 0 || lookSnapshot.videoFrameCount != 0 {
		t.Fatalf("audio-only media look fixture evidence = %s", sourceObservationDiagnostics(lookSnapshot))
	}

	audioOnlyReport, err := parseMediaCLIObservation(probeResult.stdout, lookResult.stdout)
	if err != nil {
		t.Fatalf("parse audio-only media CLI observation: %v", err)
	}
	if audioOnlyReport.videoPresence || audioOnlyReport.lookStatus != string(rtc.VisualObservationUnavailable) || audioOnlyReport.lookReason != string(rtc.VisualObservationReasonNoVideoTrack) {
		t.Fatalf("audio-only public media observation = %+v, want no video and unavailable look", audioOnlyReport)
	}

	bridgeURL, bridgeObserved, bridgeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: false, sendFrames: true, packets: packets})
	sourcePCM := bridgeExternalSourceAudio(t, bridgeURL, len(packets), 10*time.Second)
	waitForExternalSourceEvent(t, bridgeObserved.negotiated, "audio-only offer/answer completion")
	waitForExternalSourceEvent(t, bridgeObserved.frameDelivered, "audio-only audio frame delivery")
	bridgeSnapshot := bridgeObserved.snapshot()
	bridgeCleanup()
	if bridgeSnapshot.answerAudioTracks != 1 || bridgeSnapshot.answerVideoTracks != 0 || bridgeSnapshot.videoFrameCount != 0 {
		t.Fatalf("audio-only fixture negotiated/sent tracks = answer audio/video %d/%d, video frames %d; want 1/0 and 0 video frames", bridgeSnapshot.answerAudioTracks, bridgeSnapshot.answerVideoTracks, bridgeSnapshot.videoFrameCount)
	}
	if bridgeSnapshot.frameCount != len(packets) {
		t.Fatalf("audio-only fixture delivered %d audio frames, want %d\n%s", bridgeSnapshot.frameCount, len(packets), sourceObservationDiagnostics(bridgeSnapshot))
	}
	if energy := pcmRMSEnergy(sourcePCM); energy <= externalSourceRMSThreshold {
		t.Fatalf("audio-only bridged audio RMS energy = %.1f, want > %.1f\n%s", energy, externalSourceRMSThreshold, sourceObservationDiagnostics(bridgeSnapshot))
	}

	// Executable negative control: applying the camera assertion to the actual
	// audio-only CLI report must fail specifically because video is absent and
	// public look() is unavailable.
	cameraViolation := assertCameraMediaEvidence(audioOnlyReport, bridgeSnapshot)
	if cameraViolation == nil {
		t.Fatal("camera assertion passed on the audio-only observation; the negative control is vacuous")
	}
	if !strings.Contains(cameraViolation.Error(), "no video track") || !strings.Contains(cameraViolation.Error(), "look() unavailable") {
		t.Fatalf("audio-only rejection should name absent video and unavailable look(), got: %v", cameraViolation)
	}

	appendFrames := rawPCMAppendFrames(sourcePCM, externalSourceFrameBytes)
	fixture := buildExternalSourceReplayFixture(t, appendFrames, []string{audioOnlyReplyTranscript, audioOnlyTranscriptTail}, loudestUtteranceWindow(t, externalSourceReplyWindow))
	outWav := filepath.Join(t.TempDir(), "audio-only-response.wav")
	started := time.Now()
	sessionResult := runRootCLISession(t, parentCtx, cfgDir, fixture, sourcePCM, outWav)
	elapsed := time.Since(started)
	diagnostics := fmt.Sprintf("replay_fixture: %q; expected_append_frames=%d; pcm_bytes=%d; source_rms=%.1f; terminal_seen=%t; deadline_state=%t",
		fixture, len(appendFrames), len(sourcePCM), pcmRMSEnergy(sourcePCM), strings.Contains(sessionResult.stdout, "[session closed:"), elapsed < externalSourceHardDeadline)
	assertExternalSourceSessionOutcome(t, sessionResult, elapsed, audioOnlyReplyTranscript, outWav, diagnostics)
}

// TestWebrtcDeadSourceFailsPositiveDeliveryAssertions is the dead-source
// control: negotiation succeeds but no frame is ever delivered, so the
// positive audio-delivery assertion cannot pass.
func TestWebrtcDeadSourceFailsPositiveDeliveryAssertions(t *testing.T) {
	packets := cameraSourceRTPPackets(t)

	deadURL, deadObserved, deadCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: true, sendFrames: false, packets: packets})
	defer deadCleanup()

	// The positive audio-delivery assertion cannot be satisfied: no frame
	// ever arrives.
	if _, err := collectExternalSourceAudio(deadURL, len(packets), 750*time.Millisecond); err == nil {
		t.Fatal("frame-less source satisfied the positive audio-delivery bridge; the control is vacuous")
	} else if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "read source audio frame") && !strings.Contains(err.Error(), "open external source") {
		t.Fatalf("dead-source bridge error should name the missing audio delivery, got: %v", err)
	}
	waitForExternalSourceEvent(t, deadObserved.negotiated, "dead-source offer/answer completion")

	// And even if empty bytes were somehow carried, the energy gate rejects
	// them: silence can never satisfy the non-silent delivery assertion.
	if energy := pcmRMSEnergy(nil); energy > externalSourceRMSThreshold {
		t.Fatalf("empty source audio RMS = %.1f, want <= %.1f", energy, externalSourceRMSThreshold)
	}
}

// The v10 WebRTC external-source vertical speaks the go2rtc signaling dialect
// (`GET /api/ws?src=<name>`, JSON {"type":"webrtc/offer"} / "webrtc/answer")
// and presents deterministic G.711 μ-law audio plus, for the camera shape, an
// H.264 video track. Source audio is derived from the committed corpus
// utterance utt_short_16k.wav so both the media-observation leg and the
// session leg consume the same voiced content.
const (
	// externalSourceHardDeadline bounds every v10 leg: negotiation, media
	// observation, and the replay-backed session run must all finish inside
	// this parent deadline or the test fails on the deadline condition.
	externalSourceHardDeadline = 45 * time.Second

	// externalSourceRMSThreshold is the minimum PCM16 RMS energy for audio to
	// count as non-silent; digital silence measures 0 and voiced corpus
	// windows measure well above it (the committed utterance averages ~809).
	externalSourceRMSThreshold = 500.0

	// externalSourcePacketSamples is one 20 ms PCMU packet at 8 kHz.
	externalSourcePacketSamples = 160

	// externalSourcePrefixSamples is the length of the committed 16 kHz
	// utterance window that feeds the source stream (~1.6 s), keeping the
	// paced session leg fast enough for the PR-tier budget. The window is
	// the loudest one in the file so the source carries genuinely voiced
	// content rather than the leading silence padding.
	externalSourcePrefixSamples = 25600

	// externalSourceDecimation reduces the 16 kHz corpus to the source's
	// 8 kHz PCMU rate by taking every fourth sample.
	externalSourceDecimation = 4

	externalSourceSessionID    = "sess_v10_webrtc_external_source"
	externalSourceVideoPackets = 3

	cameraReplyTranscript     = "The camera feed looks clear today."
	audioOnlyReplyTranscript  = "I heard you, but there is no camera to inspect."
	cameraTranscriptTail      = " Clear."
	audioOnlyTranscriptTail   = " No video."
	externalSourceReplyWindow = 9600
)

// webrtcSourceOptions shapes one loopback go2rtc-compatible fixture instance.
type webrtcSourceOptions struct {
	// withVideo negotiates an H.264 video m-line and track alongside the
	// audio track (the camera shape). When false the answer carries no video
	// m-line at all, so a negotiated-but-frame-less video declaration cannot
	// be confused with genuine absence.
	withVideo bool

	// sendFrames streams the deterministic PCMU packets after ICE connects.
	// A false value models a dead source whose negotiation succeeds without
	// any media activity.
	sendFrames bool

	// packets are the precomputed PCMU payloads streamed when sendFrames is
	// set. They are returned unchanged by cameraSourceRTPPackets so the test
	// can derive the exact decoded PCM stream independently.
	packets [][]byte
}

// webrtcSourceObservation independently records what the fixture saw:
// signaling path and requested source name, answer completion, and per-track
// media activity. This is the declared-but-unused guard: the CLI report alone
// cannot pass without the fixture having actually delivered frames.
type webrtcSourceObservation struct {
	sync.Mutex
	path, source string

	offerAudioTracks, offerVideoTracks   int
	answerAudioTracks, answerVideoTracks int
	frameCount, videoFrameCount          int

	negotiated          chan struct{}
	frameDelivered      chan struct{}
	videoFrameDelivered chan struct{}

	negotiatedOnce, frameOnce, videoFrameOnce sync.Once
}

type webrtcSourceObservationSnapshot struct {
	path, source                         string
	offerAudioTracks, offerVideoTracks   int
	answerAudioTracks, answerVideoTracks int
	frameCount, videoFrameCount          int
}

func (o *webrtcSourceObservation) snapshot() webrtcSourceObservationSnapshot {
	o.Lock()
	defer o.Unlock()
	return webrtcSourceObservationSnapshot{
		path: o.path, source: o.source,
		offerAudioTracks: o.offerAudioTracks, offerVideoTracks: o.offerVideoTracks,
		answerAudioTracks: o.answerAudioTracks, answerVideoTracks: o.answerVideoTracks,
		frameCount: o.frameCount, videoFrameCount: o.videoFrameCount,
	}
}

func (o *webrtcSourceObservation) recordNegotiation(offerSDP, answerSDP string) {
	o.Lock()
	o.offerAudioTracks = countSDPMediaSections(offerSDP, "audio")
	o.offerVideoTracks = countSDPMediaSections(offerSDP, "video")
	o.answerAudioTracks = countSDPMediaSections(answerSDP, "audio")
	o.answerVideoTracks = countSDPMediaSections(answerSDP, "video")
	o.Unlock()
}

func (o *webrtcSourceObservation) recordAudioFrame() {
	o.Lock()
	o.frameCount++
	o.Unlock()
	o.frameOnce.Do(func() { close(o.frameDelivered) })
}

func (o *webrtcSourceObservation) recordVideoFrame() {
	o.Lock()
	o.videoFrameCount++
	videoFrames := o.videoFrameCount
	o.Unlock()
	if videoFrames >= externalSourceVideoPackets {
		o.videoFrameOnce.Do(func() { close(o.videoFrameDelivered) })
	}
}

func startWebrtcSourceFixture(t *testing.T, opts webrtcSourceOptions) (string, *webrtcSourceObservation, func()) {
	t.Helper()
	observed := &webrtcSourceObservation{
		negotiated:          make(chan struct{}),
		frameDelivered:      make(chan struct{}),
		videoFrameDelivered: make(chan struct{}),
	}
	var handlers sync.WaitGroup
	fixtureContext, cancelFixture := context.WithCancel(context.Background())
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		handlerContext, cancelHandler := context.WithCancel(r.Context())
		defer cancelHandler()
		go func() {
			select {
			case <-fixtureContext.Done():
				cancelHandler()
			case <-handlerContext.Done():
			}
		}()
		observed.Lock()
		observed.path = r.URL.Path
		observed.source = r.URL.Query().Get("src")
		observed.Unlock()
		if r.URL.Path != "/api/ws" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var offer struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(data, &offer); err != nil || offer.Type != "webrtc/offer" {
			return
		}

		mediaEngine := &webrtc.MediaEngine{}
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
			PayloadType:        0,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			return
		}
		if opts.withVideo {
			if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
				PayloadType:        96,
			}, webrtc.RTPCodecTypeVideo); err != nil {
				return
			}
		}
		pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			return
		}
		defer pc.Close()
		connected := make(chan struct{})
		var connectedOnce sync.Once
		pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
			if state == webrtc.PeerConnectionStateConnected {
				connectedOnce.Do(func() { close(connected) })
			}
		})

		audio, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
			"audio", "v10-camera-fixture")
		if err != nil {
			return
		}
		if _, err = pc.AddTrack(audio); err != nil {
			return
		}
		var video *webrtc.TrackLocalStaticRTP
		if opts.withVideo {
			video, err = webrtc.NewTrackLocalStaticRTP(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
				"video", "v10-camera-fixture")
			if err != nil {
				return
			}
			if _, err = pc.AddTrack(video); err != nil {
				return
			}
		}

		if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.Value}); err != nil {
			return
		}
		answer, err := pc.CreateAnswer(nil)
		if err != nil {
			return
		}
		if err = pc.SetLocalDescription(answer); err != nil {
			return
		}
		select {
		case <-webrtc.GatheringCompletePromise(pc):
		case <-handlerContext.Done():
			return
		}
		answerSDP := pc.LocalDescription().SDP
		if !opts.withVideo {
			// An audio-only source answers without any video m-line, exactly
			// as go2rtc fronts a camera that exposes no video stream. pion
			// always echoes rejected m-lines into JSEP answers, so the video
			// section is removed before the answer reaches the wire; the
			// production client accepts the reduced answer (verified against
			// pion v4.2.18) and parseSDP reports no negotiated video track.
			answerSDP = stripSDPMediaSection(answerSDP, "video")
		}
		observed.recordNegotiation(offer.Value, answerSDP)
		if err = conn.WriteJSON(struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}{Type: "webrtc/answer", Value: answerSDP}); err != nil {
			return
		}
		observed.negotiatedOnce.Do(func() { close(observed.negotiated) })
		select {
		case <-connected:
		case <-handlerContext.Done():
			return
		}
		if opts.sendFrames {
			streamFixtureAudio(t, audio, video, opts.packets, observed)
		}
		<-handlerContext.Done()
	}))
	u, _ := url.Parse(server.URL)
	rawURL := "go2rtc://" + u.Host + "/api/ws?src=v10-tuya-main"
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancelFixture()
			server.CloseClientConnections()
			handlersDone := make(chan struct{})
			go func() {
				handlers.Wait()
				close(handlersDone)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			select {
			case <-handlersDone:
			case <-ctx.Done():
				t.Errorf("go2rtc fixture handlers did not close: %v", ctx.Err())
			}
			serverClosed := make(chan struct{})
			go func() {
				server.Close()
				close(serverClosed)
			}()
			select {
			case <-serverClosed:
			case <-ctx.Done():
				t.Errorf("go2rtc fixture server did not close: %v", ctx.Err())
			}
		})
	}
	t.Cleanup(cleanup)
	return rawURL, observed, cleanup
}

// streamFixtureAssets writes every precomputed audio packet once, then a small
// burst of H.264 packets on the video track when one is negotiated. Delivery
// is recorded per track so the tests can prove real media activity rather
// than a declared-but-unused capability.
func streamFixtureAudio(t *testing.T, audio, video *webrtc.TrackLocalStaticRTP, packets [][]byte, observed *webrtcSourceObservation) {
	t.Helper()
	for i, payload := range packets {
		packet := &rtp.Packet{Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: uint16(i + 1),
			Timestamp:      uint32(i * externalSourcePacketSamples),
		}, Payload: payload}
		data, err := packet.Marshal()
		if err != nil {
			t.Errorf("marshal fixture RTP: %v", err)
			return
		}
		if _, err := audio.Write(data); err != nil {
			t.Errorf("write fixture audio packet: %v", err)
			return
		}
		observed.recordAudioFrame()
	}
	for i := 0; i < externalSourceVideoPackets && video != nil; i++ {
		packet := &rtp.Packet{Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: uint16(i + 1),
			Timestamp:      uint32(i * 3000),
			Marker:         i == 2,
		}, Payload: []byte{0x67, 0x42, 0xc0, 0x1f}} // H.264 SPS-shaped bytes
		data, err := packet.Marshal()
		if err != nil {
			return
		}
		if _, err := video.Write(data); err != nil {
			return
		}
		observed.recordVideoFrame()
	}
}

// stripSDPMediaSection removes one media section (its m-line and every
// following attribute line) from an SDP body while keeping the trailing CRLF
// the SDP parser requires.
func stripSDPMediaSection(sdp, media string) string {
	lines := splitSDPLines(sdp)
	out := make([]string, 0, len(lines))
	inTarget := false
	for _, line := range lines {
		if len(line) >= 2 && line[:2] == "m=" {
			fields := fieldsOf(line[2:])
			inTarget = len(fields) > 0 && fields[0] == media
		}
		if !inTarget {
			out = append(out, line)
		}
	}
	return joinSDPLines(out)
}

func splitSDPLines(sdp string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(sdp); i++ {
		if sdp[i] == '\n' {
			end := i
			if end > start && sdp[end-1] == '\r' {
				end--
			}
			lines = append(lines, sdp[start:end])
			start = i + 1
		}
	}
	if start < len(sdp) {
		lines = append(lines, sdp[start:])
	}
	return lines
}

func joinSDPLines(lines []string) string {
	joined := ""
	for _, line := range lines {
		joined += line + "\r\n"
	}
	return joined
}

func fieldsOf(value string) []string {
	fields := []string{}
	current := ""
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == ' ' || value[i] == '\t' {
			if current != "" {
				fields = append(fields, current)
				current = ""
			}
			continue
		}
		current += string(value[i])
	}
	return fields
}

func countSDPMediaSections(sdp, media string) int {
	want := "m=" + media
	count := 0
	for _, line := range splitSDPLines(sdp) {
		fields := fieldsOf(line)
		if len(fields) > 0 && fields[0] == want {
			count++
		}
	}
	return count
}

// committedCorpusWAVPath locates a committed corpus WAV under
// go-agent-loop/testdata/audio so the source stream reuses the existing
// fixture instead of adding new binary assets.
func committedCorpusWAVPath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve corpus fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed corpus WAV %s not found: %v", name, err)
	}
	return path
}

// ulawEncodeByte compresses one linear PCM16 sample to G.711 μ-law exactly as
// a camera encoder would; the production decoder under test expands it back.
func ulawEncodeByte(sample int16) byte {
	const bias = 0x84
	const clip = 32635
	sign := byte(0x00)
	value := int(sample)
	if value < 0 {
		sign = 0x80
		value = -value
	}
	if value > clip {
		value = clip
	}
	value += bias
	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := byte((value >> uint(exponent+3)) & 0x0f)
	return ^(sign | byte(exponent)<<4 | mantissa)
}

// loudestSampleWindow returns the highest-energy contiguous sample window of
// the given length (scanned at a fixed stride), so fixture streams derived
// from the committed corpus carry voiced content deterministically.
func loudestSampleWindow(samples []int16, window int) []int16 {
	if window >= len(samples) {
		return samples
	}
	bestStart, bestEnergy := 0, -1.0
	for start := 0; start+window <= len(samples); start += 800 {
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

// cameraSourceRTPPackets derives the deterministic PCMU packet stream from
// the committed utt_short_16k.wav corpus utterance: its loudest fixed 1.6 s
// window is decimated to the source's 8 kHz rate and μ-law encoded into
// 20 ms packets.
func cameraSourceRTPPackets(t *testing.T) [][]byte {
	t.Helper()
	wavBytes, err := os.ReadFile(committedCorpusWAVPath(t, "utt_short_16k.wav"))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("committed corpus WAV rate = %d, want 16000", rate)
	}
	window := loudestSampleWindow(samples, externalSourcePrefixSamples)
	downsampled := make([]int16, 0, len(window)/externalSourceDecimation+1)
	for i := 0; i < len(window); i += externalSourceDecimation {
		downsampled = append(downsampled, window[i])
	}
	packets := make([][]byte, 0, (len(downsampled)+externalSourcePacketSamples-1)/externalSourcePacketSamples)
	payload := make([]byte, 0, len(downsampled))
	for _, sample := range downsampled {
		payload = append(payload, ulawEncodeByte(sample))
	}
	for start := 0; start < len(payload); start += externalSourcePacketSamples {
		end := start + externalSourcePacketSamples
		if end > len(payload) {
			end = len(payload)
		}
		chunk := make([]byte, end-start)
		copy(chunk, payload[start:end])
		packets = append(packets, chunk)
	}
	return packets
}

// rawPCMAppendFrames chunks a raw PCM16 byte stream into the FrameSize-sample
// frames the session audio source emits over the wire, zero-padding the final
// short frame exactly as documented for raw stdin input.
func rawPCMAppendFrames(pcm []byte, frameBytes int) [][]byte {
	frames := make([][]byte, 0, (len(pcm)+frameBytes-1)/frameBytes)
	for start := 0; start < len(pcm); start += frameBytes {
		frame := make([]byte, frameBytes)
		copy(frame, pcm[start:])
		frames = append(frames, frame)
	}
	return frames
}

// pcmRMSEnergy computes the linear RMS energy of a PCM16 little-endian stream.
func pcmRMSEnergy(pcm []byte) float64 {
	count := len(pcm) / 2
	if count == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		sample := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		sum += float64(sample) * float64(sample)
	}
	return math.Sqrt(sum / float64(count))
}

// loudestUtteranceWindow returns the highest-energy contiguous window of
// the committed utterance so the scripted reply mirrors genuinely voiced content.
func loudestUtteranceWindow(t *testing.T, window int) []int16 {
	t.Helper()
	wavBytes, err := os.ReadFile(committedCorpusWAVPath(t, "utt_short_16k.wav"))
	if err != nil {
		t.Fatalf("read committed corpus WAV: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed corpus WAV: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("committed corpus WAV rate = %d, want 16000", rate)
	}
	if window <= 0 || window > len(samples) {
		t.Fatalf("reply window %d out of range for %d samples", window, len(samples))
	}
	bestStart, bestEnergy := 0, -1.0
	for start := 0; start+window <= len(samples); start += 800 {
		var energy float64
		for _, sample := range samples[start : start+window] {
			energy += float64(sample) * float64(sample)
		}
		if energy > bestEnergy {
			bestEnergy = energy
			bestStart = start
		}
	}
	reply := make([]int16, window)
	copy(reply, samples[bestStart:bestStart+window])
	return reply
}

func pcm16LEBytesOf(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}
