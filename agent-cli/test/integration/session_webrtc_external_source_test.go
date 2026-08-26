package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The v10 vertical proves, through the shipped root CLI only, that a
// go2rtc-fronted camera-style source drives a hermetic replay-backed live
// session over WebRTC. Three legs compose the proof; every claim is asserted
// from public surfaces:
//
//  1. Source bridge: the loopback go2rtc-compatible fixture negotiates with
//     the production WebRTC client (rtc.OpenMediaSource is used purely as the
//     receive pipe into the session's public `--audio-in -` stdin seam, never
//     as a substitute for a CLI assertion), and its deterministic PCMU audio
//     arrives decoded and non-silent.
//  2. Media observation: real argv (`agent media probe <url>`) through the
//     root router reports exactly one audio track (codec/rate/channels) and
//     one video track for the camera shape, or no video track for the
//     audio-only shape where look() is represented as unavailable.
//  3. Session: real argv (`agent session --replay ... --audio-in - --audio-out ...`)
//     consumes the bridged source audio byte-exactly over the record/replay
//     transport (any dropped or distorted WebRTC frame diverges the replay),
//     observes the deterministic replayed agent response, emits an explicit
//     terminal outcome, and exits successfully inside the hard deadline.
const externalSourceFrameBytes = 2 * audio.FrameSize

// mediaProbeReport mirrors the deterministic human-readable contract printed
// by `agent media probe`: Source/Audio codec/Sample rate/Channels/Video
// presence. It is the CLI-visible negotiated-track evidence for this lane.
type mediaProbeReport struct {
	source        string
	codec         string
	sampleRate    int
	channels      int
	videoPresence bool
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

// runRootCLIProbe executes actual argv against the real root router and
// returns captured stdout. It is used for the media observation leg.
func runRootCLIProbe(t *testing.T, ctx context.Context, cfgDir string, argv []string) (string, error) {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI composition: %v", err)
	}
	rootCmd := agentCLI.Generate()
	stdout := &syncBuffer{}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(argv)
	err = rootCmd.ExecuteContext(ctx)
	return stdout.String(), err
}

// runRootCLISession drives `agent session --replay <fixture> --audio-in -
// --audio-out <wav>` through the real root router with the bridged source
// audio on stdin, waiting for the explicit terminal close marker so
// asynchronous terminal formatting is always observed.
func runRootCLISession(t *testing.T, ctx context.Context, cfgDir, fixturePath string, stdinPCM []byte, outWavPath string) (string, error) {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI composition: %v", err)
	}
	rootCmd := agentCLI.Generate()
	stdout := &syncBuffer{}
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetIn(bytes.NewReader(stdinPCM))
	rootCmd.SetArgs([]string{
		"--config-dir", cfgDir,
		"session",
		"--replay", fixturePath,
		"--audio-in", "-",
		"--audio-out", outWavPath,
	})
	runErr := rootCmd.ExecuteContext(ctx)
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stdout.String(), "[session closed:") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return stdout.String(), runErr
}

// parseMediaProbeReport requires the shipped report to identify exactly one
// negotiated audio track and at most one video track. Our offer carries a
// single recvonly transceiver per direction-kind, so the single codec block
// is exactly-once track evidence; any extra or missing line breaks the count.
func parseMediaProbeReport(t *testing.T, stdout string) mediaProbeReport {
	t.Helper()
	report := mediaProbeReport{}
	counts := map[string]int{}
	lines := 0
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		switch {
		case strings.HasPrefix(line, "Source: "):
			counts["source"]++
			report.source = strings.TrimPrefix(line, "Source: ")
		case strings.HasPrefix(line, "Audio codec: "):
			counts["codec"]++
			report.codec = strings.TrimPrefix(line, "Audio codec: ")
		case strings.HasPrefix(line, "Sample rate: "):
			counts["rate"]++
			report.sampleRate, _ = strconv.Atoi(strings.TrimPrefix(line, "Sample rate: "))
		case strings.HasPrefix(line, "Channels: "):
			counts["channels"]++
			report.channels, _ = strconv.Atoi(strings.TrimPrefix(line, "Channels: "))
		case strings.HasPrefix(line, "Video presence: "):
			counts["video"]++
			report.videoPresence = strings.TrimPrefix(line, "Video presence: ") == "true"
		default:
			t.Fatalf("unexpected media probe output line %q in:\n%s", line, stdout)
		}
	}
	for field, want := range map[string]int{"source": 1, "codec": 1, "rate": 1, "channels": 1, "video": 1} {
		if counts[field] != want {
			t.Fatalf("media observation field %q appeared %d times, want exactly %d; output:\n%s", field, counts[field], want, stdout)
		}
	}
	if lines != 5 {
		t.Fatalf("media probe emitted %d output lines, want exactly 5:\n%s", lines, stdout)
	}
	return report
}

// assertCameraMediaEvidence is the shared camera-case assertion: exactly one
// PCMU/8000/mono audio track, one video track, and look() available. It is
// applied positively to the camera observation and, as the executable
// negative control, to the audio-only observation where it must fail naming
// absent video and unavailable look().
func assertCameraMediaEvidence(report mediaProbeReport) error {
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
	if !report.videoPresence {
		violations = append(violations, "no video track was negotiated")
		violations = append(violations, "look() unavailable: visual inspection cannot be served by an audio-only source")
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
// by the vertical (argv shape, stdout, exit state, deadline state).
func assertExternalSourceSessionOutcome(t *testing.T, output string, runErr error, elapsed time.Duration, wantTranscript string, outWavPath string, diagnostics string) {
	t.Helper()
	if runErr != nil {
		t.Fatalf("session command failed: %v\n%s", runErr, diagnostics)
	}
	if !strings.Contains(output, wantTranscript) {
		t.Fatalf("session output missing deterministic replayed transcript %q:\n%s\n%s", wantTranscript, output, diagnostics)
	}
	if !strings.Contains(output, "[session closed:") {
		t.Fatalf("session output missing explicit terminal outcome:\n%s\n%s", output, diagnostics)
	}
	if elapsed >= externalSourceHardDeadline {
		t.Fatalf("session exceeded hard parent deadline: %s >= %s\n%s", elapsed, externalSourceHardDeadline, diagnostics)
	}
	assertNonSilentWAVResponse(t, outWavPath, diagnostics+"\noutput:\n"+output)
}

// TestWebrtcCameraSourceDrivesReplaySessionThroughRealCLI is the v10 positive
// vertical: a loopback go2rtc-compatible camera source (PCMU audio + H.264
// video) drives a hermetic replay-backed live session end to end, with the
// agent media observation reporting the negotiated tracks through the real
// root CLI.
func TestWebrtcCameraSourceDrivesReplaySessionThroughRealCLI(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), externalSourceHardDeadline)
	defer cancelParent()
	cfgDir := t.TempDir()
	packets := cameraSourceRTPPackets(t)

	// Leg 1 — the camera's audio reaches us over WebRTC through the
	// production inbound path, non-silent and complete.
	bridgeURL, bridgeObserved, bridgeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: true, sendFrames: true, packets: packets})
	sourcePCM := bridgeExternalSourceAudio(t, bridgeURL, len(packets), 10*time.Second)
	waitForExternalSourceEvent(t, bridgeObserved.negotiated, "camera offer/answer completion")
	bridgeCleanup()
	if got := bridgeObserved.frameCount; got != len(packets) {
		t.Fatalf("fixture delivered %d audio frames, want %d", got, len(packets))
	}
	if energy := pcmRMSEnergy(sourcePCM); energy <= externalSourceRMSThreshold {
		t.Fatalf("bridged source audio RMS energy = %.1f, want > %.1f", energy, externalSourceRMSThreshold)
	}

	// Leg 2 — the CLI media observation reports the negotiated tracks.
	probeURL, probeObserved, probeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: true, sendFrames: true, packets: packets})
	stdout, err := runRootCLIProbe(t, parentCtx, cfgDir, []string{"--config-dir", cfgDir, "media", "probe", probeURL})
	if err != nil {
		t.Fatalf("media probe through real root CLI failed: %v\nstdout:\n%s", err, stdout)
	}
	report := parseMediaProbeReport(t, stdout)
	if report.source != probeURL {
		t.Fatalf("probe reported source %q, want %q (credentials-free identity)", report.source, probeURL)
	}
	if err := assertCameraMediaEvidence(report); err != nil {
		t.Fatalf("camera media observation rejected: %v\nstdout:\n%s", err, stdout)
	}
	waitForExternalSourceEvent(t, probeObserved.frameDelivered, "independent fixture frame delivery")
	probeCleanup()

	// Leg 3 — the same source audio drives the replay-backed session.
	appendFrames := rawPCMAppendFrames(sourcePCM, externalSourceFrameBytes)
	fixture := buildExternalSourceReplayFixture(t, appendFrames, []string{cameraReplyTranscript, cameraTranscriptTail}, loudestUtteranceWindow(t, externalSourceReplyWindow))
	outWav := filepath.Join(t.TempDir(), "camera-response.wav")
	started := time.Now()
	output, err := runRootCLISession(t, parentCtx, cfgDir, fixture, sourcePCM, outWav)
	elapsed := time.Since(started)
	diagnostics := fmt.Sprintf("argv: agent --config-dir %q session --replay %q --audio-in - --audio-out %q; frames=%d; pcm_bytes=%d; exit_state=%v; deadline_in=%t",
		cfgDir, fixture, outWav, len(appendFrames), len(sourcePCM), err, elapsed < externalSourceHardDeadline)
	assertExternalSourceSessionOutcome(t, output, err, elapsed, cameraReplyTranscript, outWav, diagnostics)
}

// TestWebrtcAudioOnlySourceReportsLookUnavailableThroughRealCLI proves that
// an audio-only go2rtc source keeps the session healthy while the same CLI
// path reports zero video tracks and look() unavailable, without panic, hang,
// or a false capability claim.
func TestWebrtcAudioOnlySourceReportsLookUnavailableThroughRealCLI(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), externalSourceHardDeadline)
	defer cancelParent()
	cfgDir := t.TempDir()
	packets := cameraSourceRTPPackets(t)

	bridgeURL, bridgeObserved, bridgeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: false, sendFrames: true, packets: packets})
	sourcePCM := bridgeExternalSourceAudio(t, bridgeURL, len(packets), 10*time.Second)
	waitForExternalSourceEvent(t, bridgeObserved.negotiated, "audio-only offer/answer completion")
	bridgeCleanup()
	if got := bridgeObserved.frameCount; got != len(packets) {
		t.Fatalf("audio-only fixture delivered %d audio frames, want %d", got, len(packets))
	}
	if energy := pcmRMSEnergy(sourcePCM); energy <= externalSourceRMSThreshold {
		t.Fatalf("audio-only bridged audio RMS energy = %.1f, want > %.1f", energy, externalSourceRMSThreshold)
	}

	probeURL, probeObserved, probeCleanup := startWebrtcSourceFixture(t, webrtcSourceOptions{withVideo: false, sendFrames: true, packets: packets})
	stdout, err := runRootCLIProbe(t, parentCtx, cfgDir, []string{"--config-dir", cfgDir, "media", "probe", probeURL})
	if err != nil {
		t.Fatalf("audio-only media probe must be a stable non-fatal outcome, got error: %v\nstdout:\n%s", err, stdout)
	}
	report := parseMediaProbeReport(t, stdout)
	if report.codec != "PCMU" || report.sampleRate != 8000 || report.channels != 1 {
		t.Fatalf("audio-only track facts = %s/%d/%d, want PCMU/8000/1\nstdout:\n%s", report.codec, report.sampleRate, report.channels, stdout)
	}
	if report.videoPresence {
		t.Fatalf("audio-only source falsely claims video presence:\n%s", stdout)
	}
	waitForExternalSourceEvent(t, probeObserved.frameDelivered, "audio-only fixture frame delivery")
	probeCleanup()

	// Executable negative control: the camera assertion must reject this
	// observation specifically because video is absent and look() is
	// unavailable.
	cameraViolation := assertCameraMediaEvidence(report)
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
	output, err := runRootCLISession(t, parentCtx, cfgDir, fixture, sourcePCM, outWav)
	elapsed := time.Since(started)
	diagnostics := fmt.Sprintf("argv: agent --config-dir %q session --replay %q --audio-in - --audio-out %q; frames=%d; exit_state=%v; deadline_in=%t",
		cfgDir, fixture, outWav, len(appendFrames), err, elapsed < externalSourceHardDeadline)
	assertExternalSourceSessionOutcome(t, output, err, elapsed, audioOnlyReplyTranscript, outWav, diagnostics)
}

// TestWebrtcDeadSourceFailsPositiveDeliveryAssertions is the dead-source
// control: negotiation succeeds but no frame is ever delivered, so the typed
// unreachable probe outcome fires and the positive audio-delivery assertion
// cannot pass.
func TestWebrtcDeadSourceFailsPositiveDeliveryAssertions(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), externalSourceHardDeadline)
	defer cancelParent()
	cfgDir := t.TempDir()
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

	// Through the CLI, the same dead source yields the typed unreachable
	// outcome and no successful capability report.
	stdout, err := runRootCLIProbe(t, parentCtx, cfgDir, []string{"--config-dir", cfgDir, "media", "probe", deadURL})
	if err == nil {
		t.Fatalf("dead-source probe unexpectedly succeeded:\n%s", stdout)
	}
	if !errors.Is(err, rtc.ErrSourceUnreachable) {
		t.Fatalf("dead-source probe error = %v, want typed ErrSourceUnreachable identity", err)
	}
	if strings.Contains(stdout, "Video presence:") || strings.Contains(stdout, "Audio codec:") {
		t.Fatalf("dead-source probe printed a capability report despite failure:\n%s", stdout)
	}

	// And even if empty bytes were somehow carried, the energy gate rejects
	// them: silence can never satisfy the non-silent delivery assertion.
	if energy := pcmRMSEnergy(nil); energy > externalSourceRMSThreshold {
		t.Fatalf("empty source audio RMS = %.1f, want <= %.1f", energy, externalSourceRMSThreshold)
	}
}
