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
)

func TestSessionCommandHelpExposesAudioInput(t *testing.T) {
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	help := out.String()
	if !strings.Contains(help, "--audio-in string") || !strings.Contains(help, "raw PCM16 standard input") || strings.Contains(help, ".wav") {
		t.Fatalf("session help does not describe --audio-in path and stdin behavior:\n%s", help)
	}
	if strings.Index(help, "--api-key") > strings.Index(help, "--audio-in") || strings.Index(help, "--audio-in") > strings.Index(help, "--base-url") {
		t.Fatalf("--audio-in is not alphabetically positioned with neighboring flags:\n%s", help)
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
			name:    "WAV requires incremental decoder",
			input:   services.SessionAudioInput{Path: filepath.Join(t.TempDir(), "input.wav"), Present: true},
			wantIs:  services.ErrSessionAudioInputFormat,
			wantAny: []error{audio.ErrUnsupportedFormat},
			wantMsg: ".pcm",
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
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), inferencer).Generate()
	cmd.Flags().Bool("audio-in-device", false, "owner-registered device input")
	cmd.SetArgs([]string{"--replay", "synthetic.json", "--audio-in", validPath, "--audio-in-device"})
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
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), inferencer).Generate()
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
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), inferencer).Generate()
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

func TestSessionCommandAudioInputReplaysValidatedWireFixture(t *testing.T) {
	samples := make([]int16, audio.FrameSize*2)
	for index := range samples {
		samples[index] = int16(1200 - index*2)
	}
	audioPath := filepath.Join(t.TempDir(), "fixture-input.raw")
	if err := os.WriteFile(audioPath, pcm16Bytes(samples), 0o600); err != nil {
		t.Fatal(err)
	}

	baseFixturePath := filepath.Join("..", "..", "test", "integration", "testdata", "openai_realtime_smoke.session.json")
	capture, err := gwtesting.LoadSessionCapture(baseFixturePath)
	if err != nil {
		t.Fatalf("load committed replay fixture: %v", err)
	}
	if len(capture.Records) < 2 {
		t.Fatalf("committed replay fixture has %d records, want session update and created", len(capture.Records))
	}

	// The existing committed OpenAI fixture supplies the real session.update
	// payload. Add two deterministic wire-level audio frames before a normal
	// session.closed event so ReplayWebSocketDialer validates every outbound
	// frame sent by the real agent session command.
	records := []gwtesting.CapturedSessionEvent{capture.Records[0], capture.Records[1]}
	for frameIndex := 0; frameIndex < len(samples)/audio.FrameSize; frameIndex++ {
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
	records = append(records, gwtesting.CapturedSessionEvent{
		Sequence:    len(records) + 1,
		Direction:   gwtesting.DirectionServerToClient,
		TimestampMs: int64(len(records)),
		Type:        "session.closed",
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.closed","session_id":"sess_audio_fixture","reason":"fixture_complete"}`),
	})
	capture.Session.ID = "sess_audio_fixture"
	capture.Session.FixtureProvenance = gwtesting.SessionFixtureProvenanceSynthetic
	capture.Records = records

	fixturePath := filepath.Join(t.TempDir(), "audio.session.json")
	fixtureData, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal audio replay fixture: %v", err)
	}
	if err := os.WriteFile(fixturePath, fixtureData, 0o600); err != nil {
		t.Fatalf("write audio replay fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(fixturePath); err != nil {
		t.Fatalf("audio replay fixture rejected by replay dialer: %v", err)
	}

	expectedFrames := (len(samples) + audio.FrameSize - 1) / audio.FrameSize
	audioRecords := 0
	for _, record := range capture.Records {
		if record.Type == "input_audio_buffer.append" {
			audioRecords++
		}
	}
	if audioRecords != expectedFrames {
		t.Fatalf("fixture audio frame count = %d, want %d from %d Hz/%d-sample framing", audioRecords, expectedFrames, audio.SampleRate, audio.FrameSize)
	}

	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--replay", fixturePath, "--audio-in", audioPath})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("real replay session command: %v", err)
	}

	// The fixture validator intentionally rejects raw provider-wire audio fields
	// as unsanitized. Validate the equivalent stream-message replay envelope,
	// whose base64 content is the existing sanitized session representation, and
	// run it through the generic replay inferencer for exact frame assertions.
	streamRecord := func(sequence int, direction gwtesting.SessionEventDirection, msg messages.StreamMessage) gwtesting.CapturedSessionEvent {
		payload, marshalErr := gwtesting.MarshalStreamMessage(msg)
		if marshalErr != nil {
			t.Fatalf("marshal stream replay event: %v", marshalErr)
		}
		return gwtesting.CapturedSessionEvent{
			Sequence:    sequence,
			Direction:   direction,
			TimestampMs: int64(sequence),
			Type:        string(msg.Type),
			PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
			Payload:     payload,
		}
	}
	genericRecords := []gwtesting.CapturedSessionEvent{
		streamRecord(1, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("sess_audio_fixture", "session"),
		}),
	}
	for frameIndex := 0; frameIndex < expectedFrames; frameIndex++ {
		genericRecords = append(genericRecords, streamRecord(len(genericRecords)+1, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(pcm16Bytes(samples[frameIndex*audio.FrameSize : (frameIndex+1)*audio.FrameSize])),
		}))
	}
	genericRecords = append(genericRecords, streamRecord(len(genericRecords)+1, gwtesting.DirectionServerToClient, messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("sess_audio_fixture", "fixture_complete"),
	}))
	genericCapture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic", Model: "session-replay"},
		Session:  gwtesting.SessionMetadata{ID: "sess_audio_fixture", FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic},
		Records:  genericRecords,
	}
	genericPath := filepath.Join(t.TempDir(), "audio-stream.session.json")
	genericData, err := json.MarshalIndent(genericCapture, "", "  ")
	if err != nil {
		t.Fatalf("marshal generic audio replay fixture: %v", err)
	}
	if err := os.WriteFile(genericPath, genericData, 0o600); err != nil {
		t.Fatalf("write generic audio replay fixture: %v", err)
	}
	if validationErrs := gwtesting.ValidateSessionCaptureFile(genericPath); len(validationErrs) != 0 {
		t.Fatalf("generic audio replay fixture failed validation: %v", validationErrs)
	}
	recorded := gwtesting.NewRecordingSessionInferencer(gwtesting.NewReplaySessionInferencer(genericPath))
	genericCmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), recorded).Generate()
	genericCmd.SetOut(io.Discard)
	genericCmd.SetArgs([]string{"--replay", genericPath, "--audio-in", audioPath})
	if err := genericCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("generic replay session command: %v", err)
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
		t.Fatalf("observed replay audio frames = %d, want %d", len(observedAudio), expectedFrames)
	}
	for frameIndex, got := range observedAudio {
		want := pcm16Bytes(samples[frameIndex*audio.FrameSize : (frameIndex+1)*audio.FrameSize])
		if !bytes.Equal(got, want) {
			t.Fatalf("replay audio frame %d changed order or content", frameIndex)
		}
	}
}

func TestRunSessionWithAudioInputCancellationStopsBlockingStdin(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	reader := newBlockingContextReader()
	inferencer := functional.NewMockSessionInferencer()
	t.Cleanup(inferencer.Close)
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), inferencer).Generate()
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
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), signaledInferencer).Generate()
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
