package services_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestSessionCommandHelpExposesAudioInput(t *testing.T) {
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	help := out.String()
	audioInHelp := ""
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "--audio-in string") {
			audioInHelp = line
			break
		}
	}
	if !strings.Contains(audioInHelp, "--audio-in string") || !strings.Contains(audioInHelp, "raw PCM16 standard input") || !strings.Contains(audioInHelp, ".wav") {
		t.Fatalf("session help does not describe --audio-in path and stdin behavior:\n%s", help)
	}
	if !strings.Contains(help, "--audio-in-device string") || !strings.Contains(help, "--audio-out-device string") {
		t.Fatalf("session help does not expose RTC device selectors:\n%s", help)
	}
	if strings.Index(help, "--api-key") > strings.Index(help, "--audio-in") || strings.Index(help, "--audio-in") > strings.Index(help, "--base-url") {
		t.Fatalf("--audio-in is not alphabetically positioned with neighboring flags:\n%s", help)
	}
	if strings.Index(help, "--audio-in") > strings.Index(help, "--audio-in-device") || strings.Index(help, "--audio-out") > strings.Index(help, "--audio-out-device") {
		t.Fatalf("RTC device selectors are not alphabetically positioned with their file flags:\n%s", help)
	}
}

func TestRunSessionWithAudioInputPreflightMatrix(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.raw")
	directoryPath := filepath.Join(t.TempDir(), "unreadable.raw")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	unreadablePath := filepath.Join(t.TempDir(), "unreadable-file.raw")
	if err := os.WriteFile(unreadablePath, pcm16Bytes([]int16{1}), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(unreadablePath, 0); err != nil {
			t.Fatal(err)
		}
	}
	validPath := filepath.Join(t.TempDir(), "valid.raw")
	if err := os.WriteFile(validPath, pcm16Bytes([]int16{1}), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptWAVPath := filepath.Join(t.TempDir(), "corrupt.wav")
	if err := os.WriteFile(corruptWAVPath, []byte("definitely not a RIFF wave payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		input   services.SessionAudioInput
		wantIs  error
		wantAny []error
		wantMsg string
	}{
		{
			name:    "empty path",
			input:   services.SessionAudioInput{Present: true},
			wantIs:  services.ErrSessionAudioInputEmpty,
			wantMsg: "path is empty",
		},
		{
			name:    "missing file",
			input:   services.SessionAudioInput{Path: missingPath, Present: true},
			wantIs:  services.ErrSessionAudioInputMissing,
			wantAny: []error{os.ErrNotExist},
			wantMsg: missingPath,
		},
		{
			name:    "unreadable directory",
			input:   services.SessionAudioInput{Path: directoryPath, Present: true},
			wantIs:  services.ErrSessionAudioInputUnreadable,
			wantMsg: "directory",
		},
		{
			name:    "unreadable file",
			input:   services.SessionAudioInput{Path: unreadablePath, Present: true},
			wantIs:  services.ErrSessionAudioInputUnreadable,
			wantMsg: unreadablePath,
		},
		{
			name:    "rejected format",
			input:   services.SessionAudioInput{Path: filepath.Join(t.TempDir(), "input.mp3"), Present: true},
			wantIs:  services.ErrSessionAudioInputFormat,
			wantAny: []error{audio.ErrUnsupportedFormat},
			wantMsg: ".wav, .pcm, .raw",
		},
		{
			name:    "rejected WAV format",
			input:   services.SessionAudioInput{Path: corruptWAVPath, Present: true},
			wantIs:  services.ErrSessionAudioInputFormat,
			wantAny: []error{audio.ErrUnsupportedFormat},
			wantMsg: "RIFF",
		},
		{
			name:    "audio device conflict",
			input:   services.SessionAudioInput{Path: validPath, Present: true, DevicePresent: true},
			wantIs:  services.ErrSessionAudioInputConflict,
			wantMsg: "audio device",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable file" && runtime.GOOS == "windows" {
				t.Skip("Windows does not enforce Unix permission bits")
			}
			if tc.name == "unreadable file" {
				file, openErr := os.Open(unreadablePath)
				if openErr == nil {
					_ = file.Close()
					t.Skip("test process can read mode-zero files")
				}
			}
			inferencer := &countingSessionInferencer{}
			err := services.RunSessionWithAudioInput(context.Background(), io.Discard, services.SessionRunOptions{
				ReplayPath:        "synthetic.json",
				SessionInferencer: inferencer,
			}, tc.input)
			if err == nil {
				t.Fatal("expected preflight error")
			}
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tc.wantIs)
			}
			for _, want := range tc.wantAny {
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want errors.Is(%v)", err, want)
				}
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantMsg)) {
				t.Fatalf("error = %v, want context %q", err, tc.wantMsg)
			}
			if inferencer.connects != 0 {
				t.Fatalf("preflight connected to provider %d times", inferencer.connects)
			}
			if inferencer.frames != 0 {
				t.Fatalf("preflight delivered %d audio frames", inferencer.frames)
			}
		})
	}
}

func TestSessionCommandAudioInputConflictUsesOwnerRegisteredDeviceFlag(t *testing.T) {
	validPath := filepath.Join(t.TempDir(), "valid.raw")
	if err := os.WriteFile(validPath, pcm16Bytes([]int16{1}), 0o600); err != nil {
		t.Fatal(err)
	}
	inferencer := &countingSessionInferencer{}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", validPath, "--audio-in-device", "virtual:input"})
	err := cmd.ExecuteContext(context.Background())
	if !errors.Is(err, services.ErrSessionAudioInputConflict) {
		t.Fatalf("command error = %v, want audio input conflict", err)
	}
	if inferencer.connects != 0 || inferencer.frames != 0 {
		t.Fatalf("conflicting command caused provider/frame activity: connects=%d frames=%d", inferencer.connects, inferencer.frames)
	}
}

func TestSessionCommandAudioInputStreamsExactFramesAndClosesFile(t *testing.T) {
	samples := make([]int16, audio.FrameSize*2)
	for i := range samples {
		samples[i] = int16(i*3 - 700)
	}
	path := filepath.Join(t.TempDir(), "replay.raw")
	if err := os.WriteFile(path, pcm16Bytes(samples), 0o600); err != nil {
		t.Fatal(err)
	}

	inferencer := functional.NewMockSessionInferencer()
	t.Cleanup(inferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", path})
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(context.Background()) }()

	for frameIndex := 0; frameIndex < 2; frameIndex++ {
		msg, ok := inferencer.WaitForSentMessage(messages.StreamTypeAudioDelta, 3*time.Second)
		if !ok {
			t.Fatalf("timed out waiting for audio frame %d", frameIndex)
		}
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			t.Fatalf("audio frame %d value = %T, want *messages.AudioDeltaValue", frameIndex, msg.Value)
		}
		want := pcm16Bytes(samples[frameIndex*audio.FrameSize : (frameIndex+1)*audio.FrameSize])
		if !bytes.Equal(value.Content, want) {
			t.Fatalf("audio frame %d changed order or content", frameIndex)
		}
	}
	if _, ok := inferencer.WaitForSentMessage(messages.StreamTypeAudioDelta, 100*time.Millisecond); ok {
		t.Fatal("audio source delivered a frame after EOF")
	}

	inferencer.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("session command: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for session command completion")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove input after command completion: %v", err)
	}
}

func TestSessionCommandAudioInputReadsRawStdin(t *testing.T) {
	samples := make([]int16, audio.FrameSize)
	for i := range samples {
		samples[i] = int16(-i)
	}
	inferencer := functional.NewMockSessionInferencer()
	t.Cleanup(inferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetIn(bytes.NewReader(pcm16Bytes(samples)))
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", "-"})
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(context.Background()) }()

	msg, ok := inferencer.WaitForSentMessage(messages.StreamTypeAudioDelta, 3*time.Second)
	if !ok {
		t.Fatal("timed out waiting for stdin audio frame")
	}
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok || !bytes.Equal(value.Content, pcm16Bytes(samples)) {
		t.Fatalf("stdin audio = %#v, want exact PCM16 frame", msg.Value)
	}
	if _, ok := inferencer.WaitForSentMessage(messages.StreamTypeAudioDelta, 100*time.Millisecond); ok {
		t.Fatal("stdin source delivered more than one frame")
	}
	inferencer.Close()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("session command: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stdin session completion")
	}
}

// committedSessionAudioInputWAVPath returns the committed non-empty audio
// fixture that drives every real-command audio-input replay proof.
func committedSessionAudioInputWAVPath(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourcePath), "testdata", "session-audio-input", "utterance.wav")
}

func committedSessionAudioInputStreamCapturePath(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourcePath), "testdata", "session-audio-input", "utterance-stream.session.json")
}

func TestSessionCommandAudioInputReplaysCommittedFixture(t *testing.T) {
	wavPath := committedSessionAudioInputWAVPath(t)
	capturePath := committedSessionAudioInputStreamCapturePath(t)

	// Duration-derived expectations come from the committed WAV's sample
	// contract, never from runtime-generated samples.
	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed WAV fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed WAV fixture: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("committed WAV rate = %d, want %d", rate, audio.SampleRate)
	}
	if len(samples) == 0 || len(samples)%audio.FrameSize != 0 {
		t.Fatalf("committed WAV has %d samples; want a non-empty multiple of %d", len(samples), audio.FrameSize)
	}
	expectedFrames := len(samples) / audio.FrameSize

	// The committed stream capture must stay validator-clean and carry exactly
	// one sanitized AUDIO.DELTA per committed fixture frame.
	if validationErrs := gwtesting.ValidateSessionCaptureFile(capturePath); len(validationErrs) != 0 {
		t.Fatalf("committed stream capture failed validation: %v", validationErrs)
	}
	streamCapture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load committed stream capture: %v", err)
	}
	captureAudioRecords := 0
	for _, record := range streamCapture.Records {
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == string(messages.StreamTypeAudioDelta) {
			captureAudioRecords++
		}
	}
	if captureAudioRecords != expectedFrames {
		t.Fatalf("committed capture audio frames = %d, want %d from the WAV sample contract", captureAudioRecords, expectedFrames)
	}

	// Wire-level order/content proof: derive the provider-wire capture from
	// the committed WAV and require the real command's outbound WebSocket
	// frames to match it exactly through the existing replay dialer.
	baseFixturePath := filepath.Join("..", "..", "test", "integration", "testdata", "openai_realtime_smoke.session.json")
	baseCapture, err := gwtesting.LoadSessionCapture(baseFixturePath)
	if err != nil {
		t.Fatalf("load committed replay base fixture: %v", err)
	}
	if len(baseCapture.Records) < 2 {
		t.Fatalf("committed replay base fixture has %d records, want session update and created", len(baseCapture.Records))
	}
	records := []gwtesting.CapturedSessionEvent{baseCapture.Records[0], baseCapture.Records[1]}
	for frameIndex := range expectedFrames {
		payload, marshalErr := json.Marshal(map[string]string{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(pcm16Bytes(samples[frameIndex*audio.FrameSize : (frameIndex+1)*audio.FrameSize])),
		})
		if marshalErr != nil {
			t.Fatalf("marshal audio replay event: %v", marshalErr)
		}
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        "input_audio_buffer.append",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		})
	}
	// End-of-turn regression: after the final append the client must emit
	// input_audio_buffer.commit followed by response.create, in that order,
	// before anything else.
	records = append(records,
		gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        "input_audio_buffer.commit",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(`{"type":"input_audio_buffer.commit"}`),
		},
		gwtesting.CapturedSessionEvent{
			Sequence:    len(records) + 1,
			Direction:   gwtesting.DirectionClientToServer,
			TimestampMs: int64(len(records)),
			Type:        "response.create",
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(`{"type":"response.create"}`),
		},
	)
	records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_audio_fixture","reason":"fixture_complete"}`),
	})
	baseCapture.Session.ID = "sess_audio_fixture"
	baseCapture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	baseCapture.Records = records
	wirePath := filepath.Join(t.TempDir(), "audio.session.json")
	wireData, err := json.MarshalIndent(baseCapture, "", "  ")
	if err != nil {
		t.Fatalf("marshal wire replay fixture: %v", err)
	}
	if err := os.WriteFile(wirePath, wireData, 0o600); err != nil {
		t.Fatalf("write wire replay fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(wirePath); err != nil {
		t.Fatalf("wire replay fixture rejected by replay dialer: %v", err)
	}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", wirePath, "--audio-in", wavPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("real wire replay session command: %v", err)
	}

	// Generic proof through the committed capture: the real command must
	// transmit every committed WAV frame, in order, byte for byte.
	recorded := gwtesting.NewRecordingSessionInferencer(gwtesting.NewReplaySessionInferencer(capturePath))
	genericCmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, recorded).Generate()
	genericCmd.SetOut(io.Discard)
	genericCmd.SetArgs([]string{"--replay", capturePath, "--audio-in", wavPath})
	if err := genericCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("real generic replay session command: %v", err)
	}
	var observedAudio [][]byte
	for _, event := range recorded.Recorder().Events() {
		if event.Direction != gwtesting.DirectionClientToServer || event.Type != string(messages.StreamTypeAudioDelta) {
			continue
		}
		msg, unmarshalErr := gwtesting.UnmarshalStreamMessage(event.Payload)
		if unmarshalErr != nil {
			t.Fatalf("unmarshal observed audio event: %v", unmarshalErr)
		}
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			t.Fatalf("observed audio value = %T, want *AudioDeltaValue", msg.Value)
		}
		observedAudio = append(observedAudio, value.Content)
	}
	if len(observedAudio) != expectedFrames {
		t.Fatalf("observed replay audio frames = %d, want %d from the committed fixture duration", len(observedAudio), expectedFrames)
	}
	for frameIndex, got := range observedAudio {
		want := pcm16Bytes(samples[frameIndex*audio.FrameSize : (frameIndex+1)*audio.FrameSize])
		if !bytes.Equal(got, want) {
			t.Fatalf("replay audio frame %d changed order or content against the committed fixture", frameIndex)
		}
	}
}

// TestSessionCommandWithoutAudioInputDeliversZeroFrames is the load-bearing
// wiring control: with the audio-in hook disconnected, the otherwise identical
// run through the shared session lifecycle must deliver zero audio frames to
// the same inferencer capture.
func TestSessionCommandWithoutAudioInputDeliversZeroFrames(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	capturePath := committedSessionAudioInputStreamCapturePath(t)
	recorded := gwtesting.NewRecordingSessionInferencer(gwtesting.NewReplaySessionInferencer(capturePath))
	err := services.RunSessionWithAudioInput(context.Background(), io.Discard, services.SessionRunOptions{
		ReplayPath:        capturePath,
		SessionInferencer: recorded,
	}, services.SessionAudioInput{})
	if err != nil {
		t.Fatalf("disconnected-hook session error = %v", err)
	}
	audioEvents := 0
	for _, event := range recorded.Recorder().Events() {
		if event.Direction == gwtesting.DirectionClientToServer && event.Type == string(messages.StreamTypeAudioDelta) {
			audioEvents++
		}
	}
	if audioEvents != 0 {
		t.Fatalf("disconnected audio-in hook still delivered %d audio frames", audioEvents)
	}
	assertGoroutinesSettled(t, baselineGoroutines, "no-audio control run")
}

// gatedAudioSource blocks read k>0 until send k-1 completed, so any wholesale
// read-ahead deadlocks instead of passing. It makes each-send-awaited-before-
// next-read ordering observable.
type gatedAudioSource struct {
	frames    int
	position  int
	sends     int
	eofSeen   bool
	gates     []chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
}

func newGatedAudioSource(frames int) *gatedAudioSource {
	source := &gatedAudioSource{frames: frames, closed: make(chan struct{})}
	source.gates = make([]chan struct{}, frames)
	for index := range source.gates {
		source.gates[index] = make(chan struct{})
	}
	return source
}

func (s *gatedAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	position := s.position
	s.mu.Unlock()
	if position > 0 {
		// The previous frame's send must complete before this read proceeds;
		// a wholesale reader would deadlock here instead of passing.
		select {
		case <-s.gates[position-1]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if position >= s.frames {
		s.mu.Lock()
		s.eofSeen = true
		s.mu.Unlock()
		return io.EOF
	}
	clear(buf)
	for index := range buf {
		buf[index] = int16(position*100 + index%97)
	}
	s.mu.Lock()
	s.position++
	s.mu.Unlock()
	return nil
}

func (s *gatedAudioSource) snapshot() (sends int, eof bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sends, s.eofSeen
}

func (s *gatedAudioSource) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func TestRunSessionWithAudioInputAwaitsSendBeforeNextRead(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	const totalFrames = 3
	source := newGatedAudioSource(totalFrames)
	baseInferencer := functional.NewMockSessionInferencer()
	signaledInferencer := newConnectSignaledInferencer(baseInferencer)
	t.Cleanup(baseInferencer.Close)
	var sendMu sync.Mutex
	var sent [][]byte
	firstSendObserved := make(chan struct{})
	releaseSecondRead := make(chan struct{})
	send := func(_ context.Context, pcm []byte) error {
		sendMu.Lock()
		sent = append(sent, append([]byte(nil), pcm...))
		count := len(sent)
		sendMu.Unlock()
		source.mu.Lock()
		source.sends = count
		source.mu.Unlock()
		if count == 1 {
			// Frame one is delivered; read two stays blocked until the test
			// releases it, so delivery provably began before file EOF.
			close(firstSendObserved)
			<-releaseSecondRead
			close(source.gates[0])
			return nil
		}
		close(source.gates[count-1])
		return nil
	}
	result := make(chan error, 1)
	go func() {
		result <- services.RunSessionWithAudioInput(context.Background(), io.Discard, services.SessionRunOptions{
			ReplayPath:        "synthetic.json",
			SessionInferencer: signaledInferencer,
		}, services.SessionAudioInput{
			Path:           "gated.raw",
			Present:        true,
			Source:         source,
			SendAudioInput: send,
		})
	}()

	select {
	case <-firstSendObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("first frame was not delivered before file EOF")
	}
	if _, eofSeen := source.snapshot(); eofSeen {
		t.Fatal("delivery began only after the source reached EOF")
	}
	close(releaseSecondRead)
	select {
	case <-signaledInferencer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("session inferencer did not connect")
	}
	baseInferencer.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("paced audio session error = %v", err)
		}
	case <-time.After(3 * time.Second):
		baseInferencer.Close()
		t.Fatal("timed out waiting for paced audio session completion")
	}
	sendMu.Lock()
	delivered := len(sent)
	sendMu.Unlock()
	if delivered != totalFrames {
		t.Fatalf("sent frame count = %d, want %d delivered one awaited send at a time", delivered, totalFrames)
	}
	if sends, _ := source.snapshot(); sends != totalFrames {
		t.Fatalf("send hook count = %d, want %d", sends, totalFrames)
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("audio EOF did not close its source")
	}
	assertGoroutinesSettled(t, baselineGoroutines, "paced streaming")
}

func TestRunSessionWithAudioInputCancellationStopsBlockingStdin(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	reader := newBlockingContextReader()
	inferencer := functional.NewMockSessionInferencer()
	t.Cleanup(inferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetIn(reader)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", "-"})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(ctx) }()

	select {
	case <-reader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("audio reader did not start")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled session error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation left the session command blocked")
	}
	select {
	case <-reader.returned:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation left the audio reader blocked")
	}
	assertGoroutinesSettled(t, baselineGoroutines, "cancellation")
}

func TestRunSessionWithAudioInputSessionTerminationStopsBlockingStdin(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	reader := newBlockingContextReader()
	baseInferencer := functional.NewMockSessionInferencer()
	signaledInferencer := newConnectSignaledInferencer(baseInferencer)
	t.Cleanup(baseInferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, signaledInferencer).Generate()
	cmd.SetIn(reader)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", "-"})
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(context.Background()) }()

	select {
	case <-reader.started:
	case <-time.After(2 * time.Second):
		t.Fatal("audio reader did not start")
	}
	select {
	case <-signaledInferencer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("session inferencer did not connect")
	}
	baseInferencer.Close()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("session termination error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session termination left the command blocked")
	}
	select {
	case <-reader.returned:
	case <-time.After(2 * time.Second):
		t.Fatal("session termination left the audio reader blocked")
	}
	assertGoroutinesSettled(t, baselineGoroutines, "session termination")
}

func TestRunSessionWithAudioInputRejectsUninterruptibleStdin(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	reader := newUninterruptibleReader()
	defer close(reader.release)
	inferencer := functional.NewMockSessionInferencer()
	t.Cleanup(inferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetIn(reader)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", "-"})

	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(context.Background()) }()

	select {
	case err := <-result:
		if !errors.Is(err, services.ErrSessionAudioInputUninterruptible) {
			t.Fatalf("unsupported stdin error = %v, want ErrSessionAudioInputUninterruptible", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unsupported blocking stdin left the session command blocked")
	}
	select {
	case <-reader.started:
		t.Fatal("unsupported blocking stdin was called instead of being rejected")
	default:
	}
	assertGoroutinesSettled(t, baselineGoroutines, "uninterruptible stdin")
}

func TestRunSessionWithAudioInputRejectsFailedDeadlineStdin(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	wantDeadlineErr := errors.New("deadline unsupported")
	reader := &failedDeadlineReader{deadlineErr: wantDeadlineErr, started: make(chan struct{})}
	inferencer := functional.NewMockSessionInferencer()
	t.Cleanup(inferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, inferencer).Generate()
	cmd.SetIn(reader)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", "-"})

	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(context.Background()) }()

	select {
	case err := <-result:
		if !errors.Is(err, services.ErrSessionAudioInputUninterruptible) {
			t.Fatalf("failed-deadline stdin error = %v, want ErrSessionAudioInputUninterruptible", err)
		}
		if !errors.Is(err, wantDeadlineErr) {
			t.Fatalf("failed-deadline stdin error = %v, want deadline error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed-deadline stdin left the session command blocked")
	}
	select {
	case <-reader.started:
		t.Fatal("failed-deadline stdin was read after deadline setup failed")
	default:
	}
	assertGoroutinesSettled(t, baselineGoroutines, "failed stdin deadline")
}

func TestRunSessionWithAudioInputTerminalErrorsCloseSourceExactlyOnce(t *testing.T) {
	wantReadErr := errors.New("synthetic source read failed")
	wantSendErr := errors.New("synthetic loop send failed")
	tests := []struct {
		name       string
		source     *scriptedAudioSource
		send       func(context.Context, []byte) error
		wantErr    error
		wantFrames int
	}{
		{
			name:       "eof",
			source:     newScriptedAudioSource([][]int16{{1, 2, 3}}, nil),
			wantFrames: 1,
		},
		{
			name:    "read error",
			source:  newScriptedAudioSource(nil, wantReadErr),
			wantErr: wantReadErr,
		},
		{
			name:   "send error",
			source: newScriptedAudioSource([][]int16{{4, 5, 6}}, nil),
			send: func(context.Context, []byte) error {
				return wantSendErr
			},
			wantErr: wantSendErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baselineGoroutines := runtime.NumGoroutine()
			baseInferencer := functional.NewMockSessionInferencer()
			signaledInferencer := newConnectSignaledInferencer(baseInferencer)
			t.Cleanup(baseInferencer.Close)
			var sent [][]byte
			send := tc.send
			if send == nil {
				send = func(_ context.Context, pcm []byte) error {
					sent = append(sent, append([]byte(nil), pcm...))
					return nil
				}
			}
			result := make(chan error, 1)
			go func() {
				result <- services.RunSessionWithAudioInput(context.Background(), io.Discard, services.SessionRunOptions{
					ReplayPath:        "synthetic.json",
					SessionInferencer: signaledInferencer,
				}, services.SessionAudioInput{
					Path:           "test.raw",
					Present:        true,
					Source:         tc.source,
					SendAudioInput: send,
				})
			}()
			if tc.wantErr == nil {
				select {
				case <-tc.source.closed:
				case <-time.After(2 * time.Second):
					t.Fatal("audio EOF did not close its source")
				}
				select {
				case <-signaledInferencer.connected:
				case <-time.After(2 * time.Second):
					t.Fatal("audio EOF session inferencer did not connect")
				}
				baseInferencer.Close()
			}

			var err error
			select {
			case err = <-result:
			case <-time.After(2 * time.Second):
				baseInferencer.Close()
				t.Fatal("audio terminal path did not finish")
			}
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("audio EOF error = %v", err)
				}
				if len(sent) != tc.wantFrames {
					t.Fatalf("sent frame count = %d, want %d", len(sent), tc.wantFrames)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("audio error = %v, want %v", err, tc.wantErr)
			}
			if tc.source.closeCount != 1 {
				t.Fatalf("source close count = %d, want exactly once", tc.source.closeCount)
			}
			assertGoroutinesSettled(t, baselineGoroutines, tc.name)
		})
	}
}

type blockingContextReader struct {
	started  chan struct{}
	returned chan struct{}
	once     sync.Once
}

type uninterruptibleReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type failedDeadlineReader struct {
	deadlineErr error
	started     chan struct{}
	once        sync.Once
}

type connectSignaledInferencer struct {
	inner     messages.SessionInferencer
	connected chan struct{}
	once      sync.Once
}

func newConnectSignaledInferencer(inner messages.SessionInferencer) *connectSignaledInferencer {
	return &connectSignaledInferencer{inner: inner, connected: make(chan struct{})}
}

func (i *connectSignaledInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err == nil {
		i.once.Do(func() { close(i.connected) })
	}
	return session, err
}

func newBlockingContextReader() *blockingContextReader {
	return &blockingContextReader{started: make(chan struct{}), returned: make(chan struct{})}
}

func (r *blockingContextReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("blocking reader was called without context")
}

func (r *blockingContextReader) ReadContext(ctx context.Context, _ []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	defer close(r.returned)
	<-ctx.Done()
	return 0, ctx.Err()
}

func newUninterruptibleReader() *uninterruptibleReader {
	return &uninterruptibleReader{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *uninterruptibleReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return 0, errors.New("uninterruptible reader released")
}

func (r *failedDeadlineReader) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return 0, errors.New("failed-deadline reader was called")
}

func (r *failedDeadlineReader) SetReadDeadline(time.Time) error {
	return r.deadlineErr
}

func assertGoroutinesSettled(t *testing.T, baseline int, operation string) {
	t.Helper()
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after %s = %d, baseline = %d; audio lifecycle did not settle", operation, runtime.NumGoroutine(), baseline)
}

type scriptedAudioSource struct {
	frames     [][]int16
	readErr    error
	position   int
	closeCount int
	closed     chan struct{}
	closeOnce  sync.Once
}

func newScriptedAudioSource(frames [][]int16, readErr error) *scriptedAudioSource {
	return &scriptedAudioSource{frames: frames, readErr: readErr, closed: make(chan struct{})}
}

func (s *scriptedAudioSource) ReadFrame(ctx context.Context, buf []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.position < len(s.frames) {
		clear(buf)
		copy(buf, s.frames[s.position])
		s.position++
		return nil
	}
	if s.readErr != nil {
		return s.readErr
	}
	return io.EOF
}

func (s *scriptedAudioSource) Close() error {
	s.closeOnce.Do(func() {
		s.closeCount++
		close(s.closed)
	})
	return nil
}

func pcm16Bytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}

type countingSessionInferencer struct {
	connects int
	frames   int
}

func (i *countingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return nil, errors.New("provider connection should not be attempted")
}
