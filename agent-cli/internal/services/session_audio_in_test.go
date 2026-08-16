package services_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	functional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
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
	if !strings.Contains(help, "--audio-in string") || !strings.Contains(help, "raw PCM16 standard input") {
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
			name:    "rejected format",
			input:   services.SessionAudioInput{Path: filepath.Join(t.TempDir(), "input.mp3"), Present: true},
			wantIs:  services.ErrSessionAudioInputFormat,
			wantAny: []error{audio.ErrUnsupportedFormat},
			wantMsg: ".wav, .pcm, .raw",
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
		})
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

func pcm16Bytes(samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	return data
}

type countingSessionInferencer struct {
	connects int
}

func (i *countingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects++
	return nil, errors.New("provider connection should not be attempted")
}
