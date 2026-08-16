package audio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestFileSinkRawOutputAndOwnership(t *testing.T) {
	samples := make([]int16, FrameSize)
	samples[0], samples[1], samples[2], samples[3] = -32768, 32767, -1, 1234
	path := filepath.Join(t.TempDir(), "output.RAW")
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("NewFileSink() error = %v", err)
	}
	if err := sink.WriteFrame(context.Background(), samples); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pcmBytes(samples)) {
		t.Fatalf("raw output bytes = %v, want exact PCM16 bytes", got)
	}
	if err := sink.WriteFrame(context.Background(), samples); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteFrame after Close() = %v, want ErrClosed", err)
	}
}

func TestFileSinkWAVRoundTripIsByteIdentical(t *testing.T) {
	samples := make([]int16, FrameSize*2)
	for index := range samples {
		samples[index] = int16(index*3 - 1000)
	}
	samples[0], samples[1], samples[2] = -32768, 32767, -1

	var input bytes.Buffer
	if err := wavio.Write(&input, SampleRate, samples); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "input.wav")
	outputPath := filepath.Join(t.TempDir(), "output.wav")
	if err := os.WriteFile(inputPath, input.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	source, err := NewFileSource(inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewFileSink(outputPath, nil)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	for {
		frame := make([]int16, FrameSize)
		err := source.ReadFrame(context.Background(), frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		if err := sink.WriteFrame(context.Background(), frame); err != nil {
			t.Fatalf("WriteFrame() error = %v", err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input.Bytes()) {
		t.Fatal("canonical frame-aligned WAV round trip changed bytes")
	}
}

func TestFileSinkStandardStreamAndValidation(t *testing.T) {
	t.Run("stdout is not closed", func(t *testing.T) {
		stdout := &trackingWriter{}
		sink, err := NewFileSink("-", stdout)
		if err != nil {
			t.Fatal(err)
		}
		samples := make([]int16, FrameSize)
		samples[0] = -32768
		if err := sink.WriteFrame(context.Background(), samples); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
		if stdout.closed {
			t.Fatal("sink closed caller-owned stdout")
		}
		if _, err := stdout.Write([]byte{0xaa}); err != nil {
			t.Fatal(err)
		}
		want := append(pcmBytes(samples), 0xaa)
		if !bytes.Equal(stdout.Bytes(), want) {
			t.Fatalf("stdout bytes = %v, want %v", stdout.Bytes(), want)
		}
	})

	t.Run("invalid frame and cancelled context do not write", func(t *testing.T) {
		stdout := &bytes.Buffer{}
		sink, err := NewFileSink("-", stdout)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sink.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := sink.WriteFrame(ctx, make([]int16, FrameSize)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled WriteFrame() = %v, want context.Canceled", err)
		}
		if stdout.Len() != 0 {
			t.Fatal("cancelled WriteFrame() wrote bytes")
		}
		var sizeErr *FrameSizeError
		if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize-1)); !errors.As(err, &sizeErr) {
			t.Fatalf("invalid WriteFrame() = %v, want FrameSizeError", err)
		}
		if stdout.Len() != 0 {
			t.Fatal("invalid WriteFrame() wrote bytes")
		}
	})

	t.Run("nil stdout and unsupported path are rejected before output", func(t *testing.T) {
		if _, err := NewFileSink("-", nil); !errors.Is(err, ErrNilStream) {
			t.Fatalf("nil stdout error = %v, want ErrNilStream", err)
		}
		path := filepath.Join(t.TempDir(), "not-audio.mp3")
		if _, err := NewFileSink(path, nil); !errors.Is(err, ErrUnsupportedFormat) {
			t.Fatalf("unsupported sink error = %v, want ErrUnsupportedFormat", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported sink path stat error = %v, want os.ErrNotExist", err)
		}
	})
}

func TestFileSinkUnderlyingErrors(t *testing.T) {
	samples := make([]int16, FrameSize)

	t.Run("short writes are completed", func(t *testing.T) {
		writer := &shortWriter{max: 13}
		sink := &FileSink{path: "-", format: formatRaw, writer: writer}
		if err := sink.WriteFrame(context.Background(), samples); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(writer.Bytes(), pcmBytes(samples)) {
			t.Fatal("short writer did not receive all PCM bytes")
		}
	})

	t.Run("writer error preserves identity", func(t *testing.T) {
		wantErr := errors.New("write failed")
		sink := &FileSink{path: "-", format: formatRaw, writer: &errorWriter{err: wantErr}}
		err := sink.WriteFrame(context.Background(), samples)
		if !errors.Is(err, wantErr) {
			t.Fatalf("WriteFrame() = %v, want underlying write error", err)
		}
		var streamErr *StreamError
		if !errors.As(err, &streamErr) || streamErr.Operation != "write" {
			t.Fatalf("error = %v, want write StreamError", err)
		}
	})

	t.Run("zero and invalid writes are short-write errors", func(t *testing.T) {
		for name, writer := range map[string]io.Writer{
			"zero":    zeroWriter{},
			"invalid": invalidCountWriter{},
		} {
			t.Run(name, func(t *testing.T) {
				sink := &FileSink{path: "-", format: formatRaw, writer: writer}
				if !errors.Is(sink.WriteFrame(context.Background(), samples), io.ErrShortWrite) {
					t.Fatalf("WriteFrame() did not return io.ErrShortWrite")
				}
			})
		}
	})

	t.Run("close error is idempotent", func(t *testing.T) {
		wantErr := errors.New("close failed")
		sink := &FileSink{path: "out.raw", format: formatRaw, writer: io.Discard, closer: closeError{err: wantErr}}
		first := sink.Close()
		if !errors.Is(first, wantErr) {
			t.Fatalf("first Close() = %v, want close error", first)
		}
		second := sink.Close()
		if !errors.Is(second, wantErr) {
			t.Fatalf("second Close() = %v, want same close error", second)
		}
	})

	t.Run("empty WAV close exposes codec error", func(t *testing.T) {
		sink := &FileSink{path: "empty.wav", format: formatWAV, writer: &bytes.Buffer{}}
		if !errors.Is(sink.Close(), wavio.ErrEmptySamples) {
			t.Fatalf("Close() = %v, want wavio.ErrEmptySamples", sink.Close())
		}
	})
}

type trackingWriter struct {
	bytes.Buffer
	closed bool
}

func (w *trackingWriter) Close() error {
	w.closed = true
	return nil
}

type shortWriter struct {
	bytes.Buffer
	max int
}

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > w.max {
		data = data[:w.max]
	}
	return w.Buffer.Write(data)
}

type errorWriter struct{ err error }

func (w *errorWriter) Write([]byte) (int, error) { return 0, w.err }

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type invalidCountWriter struct{}

func (invalidCountWriter) Write(data []byte) (int, error) { return len(data) + 1, nil }

type closeError struct{ err error }

func (c closeError) Close() error { return c.err }
