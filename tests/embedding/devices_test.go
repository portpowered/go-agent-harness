package embedding_test

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type embeddedFileSink struct {
	closes  int
	samples []int16
}

func (*embeddedFileSink) WriteFrame(context.Context, []int16) error { return nil }
func (sink *embeddedFileSink) WriteSamples(_ context.Context, samples []int16) error {
	sink.samples = append(sink.samples, samples...)
	return nil
}
func (sink *embeddedFileSink) Close() error {
	sink.closes++
	return nil
}

type embeddedFrameInput struct{ frames []audio.PCMFrame }

func (*embeddedFrameInput) Close() error { return nil }
func (input *embeddedFrameInput) ReadFrame(context.Context) (audio.PCMFrame, error) {
	if len(input.frames) == 0 {
		return audio.PCMFrame{}, io.EOF
	}
	frame := input.frames[0]
	input.frames = input.frames[1:]
	return frame, nil
}

func TestExternalFilePlaybackPreservesMultipleResponseTails(t *testing.T) {
	sink := &embeddedFileSink{}
	handle, err := devicewire.NewFileService().Open(context.Background(), devices.Request{
		PlaybackEnabled: true, SampleRate: 24000,
		FileOutput: &devices.FileOutput{Sink: sink, SampleRate: 24000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	input := &embeddedFrameInput{frames: []audio.PCMFrame{
		{Samples: []int16{1, -2, 3}, Format: audio.PCM16DeviceFormat(24000), StreamID: "first", EndOfResponse: true},
		{Samples: []int16{4, -5}, Format: audio.PCM16DeviceFormat(24000), StreamID: "second", EndOfResponse: true},
	}}
	if err := handle.Media().Playback.Pump(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if want := []int16{1, -2, 3, 4, -5}; !reflect.DeepEqual(sink.samples, want) {
		t.Fatalf("file samples=%v, want exact response tails %v", sink.samples, want)
	}
}

func TestExternalFilePlaybackDoesNotAdmitCapture(t *testing.T) {
	sink := &embeddedFileSink{}
	host := devicewire.NewFileService()
	handle, err := host.Open(context.Background(), devices.Request{
		PlaybackEnabled: true,
		FileOutput:      &devices.FileOutput{Sink: sink, SampleRate: 24000},
		SampleRate:      24000,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	ports := handle.Media()
	if ports.Capture != nil {
		t.Fatal("playback-only admission returned a capture interface")
	}
	if ports.Playback == nil {
		t.Fatal("playback-only admission omitted playback")
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.closes != 1 {
		t.Fatalf("sink close count=%d, want 1", sink.closes)
	}
}
