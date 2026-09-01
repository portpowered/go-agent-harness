//go:build !nomicrophone && cgo

package audio

import (
	"context"
	"testing"
)

func TestCaptureOverflowPolicyAndSequenceGap(t *testing.T) {
	m := &MicrophoneSource{frameCh: make(chan []int16, 64), stats: CaptureQueueStats{DropPolicy: "drop_oldest"}}
	for sequence := int16(1); sequence <= 65; sequence++ {
		frame := make([]int16, FrameSize)
		for i := range frame {
			frame[i] = sequence
		}
		raw := make([]byte, FrameSize*2)
		encodePCM16(raw, frame)
		m.onCapture(raw, FrameSize)
	}
	stats := m.CaptureStats()
	if stats.DroppedFrames != 1 || stats.DroppedSamples != FrameSize || stats.SequenceGaps != 1 || stats.QueuedSamples != 64*FrameSize || stats.DropPolicy != "drop_oldest" {
		t.Fatalf("capture stats = %+v", stats)
	}
	got := make([]int16, FrameSize)
	if err := m.ReadFrame(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if got[0] != 2 {
		t.Fatalf("oldest retained sequence = %d, want 2", got[0])
	}
}

func TestCapturePartialAndZeroCallbackAreSafe(t *testing.T) {
	m := &MicrophoneSource{frameCh: make(chan []int16, 1), stats: CaptureQueueStats{DropPolicy: "drop_oldest"}}
	m.onCapture(nil, 0)
	m.onCapture([]byte{1, 0, 2, 0}, 99)
	if got := m.CaptureStats().CapturedSamples; got != 2 {
		t.Fatalf("captured samples = %d, want 2", got)
	}
}
