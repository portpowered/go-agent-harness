package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"
)

const processHandleCountSettleTolerance = 1

func processOpenHandleCount(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("S9 open-handle count skipped: /proc/self/fd is unavailable on %s", runtime.GOOS)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("S9 open-handle count skipped: cannot read /proc/self/fd: %v", err)
	}
	return len(entries)
}

func settledProcessOpenHandleCount(t *testing.T, want int) int {
	t.Helper()
	last := want
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		last = processOpenHandleCount(t)
		if withinHandleCountTolerance(last, want) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

func assertHandleCountWithinTolerance(t *testing.T, got, want int, operation string) {
	t.Helper()
	if !withinHandleCountTolerance(got, want) {
		t.Fatalf("open-handle count after %s = %d, want %d +/- %d", operation, got, want, processHandleCountSettleTolerance)
	}
}

func withinHandleCountTolerance(got, want int) bool {
	delta := got - want
	if delta < 0 {
		delta = -delta
	}
	return delta <= processHandleCountSettleTolerance
}

func assertSourceFrames(t *testing.T, source audio.AudioSource, samples []int16) {
	t.Helper()
	wantFrames := (len(samples) + audio.FrameSize - 1) / audio.FrameSize
	for frameIndex := 0; frameIndex < wantFrames; frameIndex++ {
		buf := make([]int16, audio.FrameSize)
		for index := range buf {
			buf[index] = 12345
		}
		if err := source.ReadFrame(context.Background(), buf); err != nil {
			t.Fatalf("ReadFrame(%d) error = %v", frameIndex, err)
		}

		want := make([]int16, audio.FrameSize)
		start := frameIndex * audio.FrameSize
		copy(want, samples[start:min(start+audio.FrameSize, len(samples))])
		if !reflect.DeepEqual(buf, want) {
			t.Fatalf("ReadFrame(%d) = %v, want %v", frameIndex, buf, want)
		}
	}

	buf := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame after %d frames = %v, want io.EOF", wantFrames, err)
	}
}
