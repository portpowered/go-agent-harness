package devices

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func assertSourceFrames(t *testing.T, source audio.AudioSource, samples []int16) {
	t.Helper()
	wantFrames := (len(samples) + audio.FrameSize - 1) / audio.FrameSize
	for frameIndex := range wantFrames {
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

// closeForTest closes a test-owned resource and reports a close failure
// without aborting the remaining deferred cleanup.
func closeForTest(tb testing.TB, name string, closer io.Closer) {
	tb.Helper()
	if err := closer.Close(); err != nil {
		tb.Errorf("close %s: %v", name, err)
	}
}

// constantDevice builds a device descriptor from test-constant identifiers.
// Invalid constants are a fixture bug, not a runtime state.
func constantDevice(backend, nativeID, name string, direction Direction) Device {
	device, err := NewDevice(backend, nativeID, name, direction)
	if err != nil {
		panic(fmt.Sprintf("devices: invalid constant device %s/%s: %v", backend, nativeID, err))
	}
	return device
}

// noErrorForTest fails the test immediately on an unexpected error.
func noErrorForTest(tb testing.TB, err error) {
	tb.Helper()
	if err != nil {
		tb.Fatal(err)
	}
}

// simulatedStreamForTest opens one simulated duplex stream and fails the test
// when the registry refuses it or returns another handle type.
func simulatedStreamForTest(tb testing.TB, r *SimulatedDuplexRegistry, id DeviceID) *SimulatedDuplexStream {
	tb.Helper()
	opened, err := r.Open(id)
	if err != nil {
		tb.Fatalf("open simulated %s: %v", id, err)
	}
	stream, ok := opened.(*SimulatedDuplexStream)
	if !ok {
		tb.Fatalf("open simulated %s returned %T", id, opened)
	}
	return stream
}

// virtualStreamForTest narrows a virtual registry handle to its stream type.
func virtualStreamForTest(tb testing.TB, opened OpenedDevice) *VirtualStream {
	tb.Helper()
	stream, ok := opened.(*VirtualStream)
	if !ok {
		tb.Fatalf("virtual registry returned %T", opened)
	}
	return stream
}

func int16Samples(start, count int) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = int16(start + index)
	}
	return samples
}
