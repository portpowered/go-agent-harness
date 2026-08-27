package room

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestPCM16MixerMixesEveryActiveInputAndClips(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 10 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()

	for _, id := range []string{"alpha", "beta", "gamma"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := mixer.Write("alpha", pcm16(1000, -1000)); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := mixer.Write("beta", pcm16(2000, -2000)); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := mixer.Write("gamma", pcm16(30000, -30000)); err != nil {
		t.Fatalf("write gamma: %v", err)
	}

	want := pcm16(32767, -32768, 0, 0, 0, 0, 0, 0, 0, 0)
	got := readMixerFrame(t, mixer, want)
	if !bytes.Equal(got, want) {
		t.Fatalf("mixed frame = %v, want %v", decodePCM16(got), decodePCM16(want))
	}
}

func TestPCM16MixerPreservesPartialInputAcrossCadenceFrames(t *testing.T) {
	format := PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: 20 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()
	if err := mixer.AddInput("speaker"); err != nil {
		t.Fatalf("add input: %v", err)
	}
	if err := mixer.Write("speaker", pcm16(1)); err != nil {
		t.Fatalf("write first partial chunk: %v", err)
	}
	if err := mixer.Write("speaker", pcm16(2, 3, 4)); err != nil {
		t.Fatalf("write second partial chunk: %v", err)
	}

	readMixerFrame(t, mixer, pcm16(1, 2))
	readMixerFrame(t, mixer, pcm16(3, 4))
}

func TestPCM16MixerRemovalDiscardsOnlyRemovedInput(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 10 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  4,
		OutputQueueFrames: 4,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()
	for _, id := range []string{"keep", "remove"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := mixer.Write("keep", pcm16(5)); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := mixer.Write("remove", pcm16(7)); err != nil {
		t.Fatalf("write remove: %v", err)
	}
	if err := mixer.RemoveInput("remove"); err != nil {
		t.Fatalf("remove input: %v", err)
	}
	readMixerFrame(t, mixer, pcm16(5, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	if got := mixer.Inputs(); len(got) != 1 || got[0] != "keep" {
		t.Fatalf("active inputs = %v, want [keep]", got)
	}
}

func TestPCM16MixerCancellationUnblocksReadFrame(t *testing.T) {
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            PCM16Format{SampleRate: 100, Channels: 1, FrameDuration: time.Second},
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	defer mixer.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	readCancel()
	_, err = mixer.ReadFrame(readCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read after cancellation error = %v, want cancellation", err)
	}
}

func readMixerFrame(t *testing.T, mixer *PCM16Mixer, want []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		frame, err := mixer.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("read mixer frame: %v", err)
		}
		if bytes.Equal(frame, want) {
			return frame
		}
	}
}

func pcm16(samples ...int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:index*2+2], uint16(sample))
	}
	return pcm
}

func decodePCM16(pcm []byte) []int16 {
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2 : index*2+2]))
	}
	return samples
}
