package fileports

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegateway "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func cliLabels() devices.FileMediaLabels {
	return devices.FileMediaLabels{Input: "--audio-in", InputTurn: "--audio-in-turn", Interruption: "--audio-interrupt", Output: "--audio-out"}
}

func writePCM(t *testing.T, name string, samples int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, make([]byte, samples*2), 0o600); err != nil {
		t.Fatalf("write pcm: %v", err)
	}
	return path
}

func TestOpenFileMediaWithoutMediaReturnsNilHandle(t *testing.T) {
	handle, err := New().OpenFileMedia(devices.FileMediaRequest{})
	if err != nil || handle != nil {
		t.Fatalf("OpenFileMedia(empty) = (%v, %v), want (nil, nil)", handle, err)
	}
}

func TestOpenFileMediaAdmitsRolesWithPacingSchedulerAndObservation(t *testing.T) {
	input := writePCM(t, "in.pcm", audio.FrameSize)
	turn := writePCM(t, "turn.pcm", audio.FrameSize)
	interrupt := writePCM(t, "interrupt.pcm", audio.FrameSize)
	output := filepath.Join(t.TempDir(), "out.pcm")
	scheduler := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
	observed := map[int]int{}
	handle, err := New().OpenFileMedia(devices.FileMediaRequest{
		Input:         &devices.FileMediaSource{Path: input, SampleRate: 24000},
		InputTurns:    []string{turn},
		Interruptions: []string{interrupt},
		OutputPath:    output, OutputSampleRate: 24000,
		Scheduler: scheduler, FrameInput: true,
		ObserveSource: func(source audio.AudioSource, rate int) audio.AudioSource {
			observed[rate]++
			return source
		},
	})
	if err != nil {
		t.Fatalf("OpenFileMedia: %v", err)
	}
	assertAdmittedRoles(t, handle.Media(), scheduler)
	if observed[24000] != 1 || observed[audio.SampleRate] != 1 {
		t.Fatalf("observed sources = %v, want primary input and one turn", observed)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func assertAdmittedRoles(t *testing.T, media devices.FileMedia, scheduler clock.Scheduler) {
	t.Helper()
	if media.Input == nil || media.Input.SampleRate != 24000 || !media.Input.Pace || media.Input.Continuous || media.Input.Scheduler != scheduler {
		t.Fatalf("input = %+v", media.Input)
	}
	if _, ok := media.Input.Source.(*frameAudioSource); !ok {
		t.Fatalf("framed input source = %T, want fixed-frame source", media.Input.Source)
	}
	if len(media.InputTurns) != 1 || media.InputTurns[0].SampleRate != audio.SampleRate || media.InputTurns[0].Scheduler != scheduler {
		t.Fatalf("turns = %+v", media.InputTurns)
	}
	if len(media.Interruptions) != 1 || !media.Interruptions[0].Pace || media.Interruptions[0].Scheduler != scheduler {
		t.Fatalf("interruptions = %+v", media.Interruptions)
	}
	if media.Output == nil || media.Output.SampleRate != 24000 || media.Output.Continuous {
		t.Fatalf("output = %+v", media.Output)
	}
}

func TestOpenFileMediaLabelsFailuresAndClosesEarlierPorts(t *testing.T) {
	input := writePCM(t, "in.pcm", audio.FrameSize)
	missing := filepath.Join(t.TempDir(), "missing.wav")
	for _, test := range []struct {
		name    string
		request devices.FileMediaRequest
		want    string
	}{
		{name: "input", request: devices.FileMediaRequest{Input: &devices.FileMediaSource{Path: missing}, Labels: cliLabels()}, want: `--audio-in "` + missing + `": open`},
		{name: "turn", request: devices.FileMediaRequest{Input: &devices.FileMediaSource{Path: input}, InputTurns: []string{missing}, Labels: cliLabels()}, want: `--audio-in-turn 1 "` + missing + `": --audio-in "` + missing + `"`},
		{name: "interrupt", request: devices.FileMediaRequest{Interruptions: []string{missing}, Labels: cliLabels()}, want: `--audio-interrupt 1 "` + missing + `"`},
		{name: "output", request: devices.FileMediaRequest{Input: &devices.FileMediaSource{Path: input}, OutputPath: filepath.Join(missing, "out.wav"), Labels: cliLabels()}, want: `--audio-out "`},
		{name: "neutral", request: devices.FileMediaRequest{Interruptions: []string{missing}}, want: `audio interruption 1 "` + missing + `": audio input "`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handle, err := New().OpenFileMedia(test.request)
			if err == nil || handle != nil {
				t.Fatalf("OpenFileMedia = (%v, %v), want failure", handle, err)
			}
			if !strings.HasPrefix(err.Error(), test.want) {
				t.Fatalf("error = %q, want prefix %q", err, test.want)
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("error %v does not retain os.ErrNotExist", err)
			}
		})
	}
}

func TestOpenFileMediaReadsWAVRateAndStreamsStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(path, wavFile(t, 16000, audio.FrameSize), 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
	var stdout bytes.Buffer
	handle, err := New().OpenFileMedia(devices.FileMediaRequest{Input: &devices.FileMediaSource{Path: path}, OutputPath: "-", Stdout: &stdout})
	if err != nil {
		t.Fatalf("OpenFileMedia: %v", err)
	}
	media := handle.Media()
	if media.Input.SampleRate != 16000 {
		t.Fatalf("wav input rate = %d, want 16000", media.Input.SampleRate)
	}
	if !media.Output.Continuous {
		t.Fatal("stdout output must be continuous")
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func wavFile(t *testing.T, rate, samples int) []byte {
	t.Helper()
	var out bytes.Buffer
	dataSize := samples * 2
	write := func(value any) {
		if err := binary.Write(&out, binary.LittleEndian, value); err != nil {
			t.Fatalf("write wav header: %v", err)
		}
	}
	out.WriteString("RIFF")
	write(uint32(36 + dataSize))
	out.WriteString("WAVEfmt ")
	write(uint32(16))
	write(uint16(1))
	write(uint16(1))
	write(uint32(rate))
	write(uint32(rate * 2))
	write(uint16(2))
	write(uint16(16))
	out.WriteString("data")
	write(uint32(dataSize))
	out.Write(make([]byte, dataSize))
	return out.Bytes()
}

func TestOpenFileMediaStdinInterruptibleCloseKeepsCallerDescriptor(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	t.Cleanup(func() {
		if err := errors.Join(read.Close(), write.Close()); err != nil {
			t.Errorf("close pipe: %v", err)
		}
	})
	handle, err := New().OpenFileMedia(devices.FileMediaRequest{Input: &devices.FileMediaSource{Path: "-", Stdin: read, CloseStdinOnCancel: true}})
	if err != nil {
		t.Fatalf("OpenFileMedia: %v", err)
	}
	input := handle.Media().Input
	if !input.Continuous || input.Pace {
		t.Fatalf("stdin input = %+v, want continuous unpaced", input)
	}
	readErr := make(chan error, 1)
	go func() {
		source, ok := input.Source.(audio.SampleSource)
		if !ok {
			readErr <- errors.New("stdin source does not support sample-count reads")
			return
		}
		_, err := source.ReadSamples(context.Background(), make([]int16, audio.FrameSize))
		readErr <- err
	}()
	if err := handle.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("read after close succeeded, want a closed-source failure")
		}
	case <-time.After(time.Second):
		t.Fatal("closing the handle did not stop the blocked stdin read")
	}
}

func TestInterruptibleAudioSourceCloseStopsBlockedRead(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	t.Cleanup(func() {
		if err := read.Close(); err != nil {
			t.Errorf("close pipe reader: %v", err)
		}
	})
	t.Cleanup(func() {
		if err := write.Close(); err != nil {
			t.Errorf("close pipe writer: %v", err)
		}
	})

	duplicate, err := devicegateway.OpenInterruptibleInput(read)
	if err != nil {
		t.Fatalf("duplicate input: %v", err)
	}
	source, err := audio.NewFileSource("-", duplicate)
	if err != nil {
		if closeErr := duplicate.Close(); closeErr != nil {
			t.Fatalf("create file source: %v (close duplicate: %v)", err, closeErr)
		}
		t.Fatalf("create file source: %v", err)
	}
	interruptible := &interruptibleAudioSource{source: source, input: duplicate}

	readErr := make(chan error, 1)
	go func() {
		_, readErrValue := interruptible.ReadSamples(context.Background(), make([]int16, audio.FrameSize))
		readErr <- readErrValue
	}()
	select {
	case err := <-readErr:
		t.Fatalf("blocked input read returned before close: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	if err := interruptible.Close(); err != nil {
		t.Fatalf("close interruptible source: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("input read error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing interruptible source did not stop blocked read")
	}

	if _, err := write.Write([]byte{0x01}); err != nil {
		t.Fatalf("write through caller-owned input after duplicate close: %v", err)
	}
	var sample [1]byte
	if count, err := read.Read(sample[:]); err != nil || count != 1 || sample[0] != 0x01 {
		t.Fatalf("caller-owned input after duplicate close = (%d, %v, %x), want one byte", count, err, sample[0])
	}
}
