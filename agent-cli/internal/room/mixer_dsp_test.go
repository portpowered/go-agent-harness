package room

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"
)

func TestPCMMixLegacyUsesSharedFinalClipAndSortedAttribution(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		OutputQueueFrames: 2,
		InputQueueFrames:  2,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	for _, id := range []string{"gamma", "alpha", "beta"} {
		if err := mixer.AddInput(id); err != nil {
			t.Fatalf("add input %s: %v", id, err)
		}
	}
	if err := mixer.Write("alpha", pcm16(32767, 30000, -32768, 1)); err != nil {
		t.Fatalf("write alpha: %v", err)
	}
	if err := mixer.Write("beta", pcm16(32767, 10000, -32768, 2)); err != nil {
		t.Fatalf("write beta: %v", err)
	}
	if err := mixer.Write("gamma", pcm16(-32768, -32768, 32767, 0)); err != nil {
		t.Fatalf("write gamma: %v", err)
	}
	if err := mixer.Advance(context.Background()); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, err := mixer.ReadFrameWithSources(context.Background())
	if err != nil {
		t.Fatalf("read mixed frame: %v", err)
	}
	wantPCM := pcm16(32766, 7232, -32768, 3)
	if !bytes.Equal(got.PCM, wantPCM) {
		t.Fatalf("mixed samples = %v, want %v", decodePCM16(got.PCM), decodePCM16(wantPCM))
	}
	if !reflect.DeepEqual(got.Sources, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("sources = %v, want sorted contributing inputs", got.Sources)
	}
}

func TestPCMMixLegacyKeepsFullCadenceZeroPadding(t *testing.T) {
	format := PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 4 * time.Millisecond}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		OutputQueueFrames: 1,
		InputQueueFrames:  2,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("new mixer: %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	if err := mixer.AddInput("speaker"); err != nil {
		t.Fatalf("add input: %v", err)
	}
	if err := mixer.Write("speaker", pcm16(7, 8)); err != nil {
		t.Fatalf("write short input: %v", err)
	}
	if err := mixer.Advance(context.Background()); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, err := mixer.ReadFrameWithSources(context.Background())
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	want := pcm16(7, 8, 0, 0)
	if !bytes.Equal(got.PCM, want) || !reflect.DeepEqual(got.Sources, []string{"speaker"}) {
		t.Fatalf("short legacy frame = %v sources=%v, want zero-padded frame and speaker", decodePCM16(got.PCM), got.Sources)
	}
}
