package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSourceConformancePartialFinalFrame runs the same observable framing
// contract against the in-memory and file-backed sources.
func TestSourceConformancePartialFinalFrame(t *testing.T) {
	samples := make([]int16, FrameSize*2+7)
	for index := range samples {
		samples[index] = int16(index - 600)
	}
	samples[0] = -32768
	samples[len(samples)-1] = 32767

	tests := []struct {
		name  string
		open  func(*testing.T) AudioSource
		close func(*testing.T)
	}{
		{name: "slice", open: func(*testing.T) AudioSource { return NewSliceSource(samples) }},
	}

	tempDir := t.TempDir()
	rawPath := filepath.Join(tempDir, "conformance.PCM")
	if err := os.WriteFile(rawPath, pcmBytes(samples), 0o600); err != nil {
		t.Fatalf("write conformance input: %v", err)
	}
	tests = append(tests, struct {
		name  string
		open  func(*testing.T) AudioSource
		close func(*testing.T)
	}{
		name: "file",
		open: func(t *testing.T) AudioSource {
			source, err := NewFileSource(rawPath, bytes.NewReader(nil))
			if err != nil {
				t.Fatalf("NewFileSource() error = %v", err)
			}
			return source
		},
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := test.open(t)
			defer source.Close()
			assertSourceFrames(t, source, samples)
		})
	}
}

func assertSourceFrames(t *testing.T, source AudioSource, samples []int16) {
	t.Helper()
	wantFrames := (len(samples) + FrameSize - 1) / FrameSize
	for frameIndex := 0; frameIndex < wantFrames; frameIndex++ {
		buf := make([]int16, FrameSize)
		for index := range buf {
			buf[index] = 12345
		}
		if err := source.ReadFrame(context.Background(), buf); err != nil {
			t.Fatalf("ReadFrame(%d) error = %v", frameIndex, err)
		}

		want := make([]int16, FrameSize)
		start := frameIndex * FrameSize
		copy(want, samples[start:minInt(start+FrameSize, len(samples))])
		if !reflect.DeepEqual(buf, want) {
			t.Fatalf("ReadFrame(%d) = %v, want %v", frameIndex, buf, want)
		}
	}

	buf := make([]int16, FrameSize)
	if err := source.ReadFrame(context.Background(), buf); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame after %d frames = %v, want io.EOF", wantFrames, err)
	}
}

func pcmBytes(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
