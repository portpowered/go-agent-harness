//go:build !nomicrophone && cgo

package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func TestCaptureOverflowPolicyAndSequenceGap(t *testing.T) {
	m := &MicrophoneSource{frameCh: make(chan []int16, 64), stats: audio.CaptureQueueStats{DropPolicy: "drop_oldest"}}
	for sequence := int16(1); sequence <= 65; sequence++ {
		frame := make([]int16, audio.FrameSize)
		for i := range frame {
			frame[i] = sequence
		}
		raw := make([]byte, audio.FrameSize*2)
		if err := codec.EncodePCM16Into(raw, frame); err != nil {
			t.Fatal(err)
		}
		m.onCapture(raw, audio.FrameSize)
	}
	stats := m.CaptureStats()
	if stats.DroppedFrames != 1 || stats.DroppedSamples != audio.FrameSize || stats.SequenceGaps != 1 || stats.QueuedSamples != 64*audio.FrameSize || stats.DropPolicy != "drop_oldest" {
		t.Fatalf("capture stats = %+v", stats)
	}
	got := make([]int16, audio.FrameSize)
	if err := m.ReadFrame(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if got[0] != 2 {
		t.Fatalf("oldest retained sequence = %d, want 2", got[0])
	}
}

func TestCapturePartialAndZeroCallbackAreSafe(t *testing.T) {
	m := &MicrophoneSource{frameCh: make(chan []int16, 1), stats: audio.CaptureQueueStats{DropPolicy: "drop_oldest"}}
	m.onCapture(nil, 0)
	m.onCapture([]byte{1, 0, 2, 0}, 99)
	if got := m.CaptureStats().CapturedSamples; got != 2 {
		t.Fatalf("captured samples = %d, want 2", got)
	}
}
