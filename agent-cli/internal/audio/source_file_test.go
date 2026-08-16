package audio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestFileSourceFormatsAndFailures(t *testing.T) {
	t.Run("unsupported extension is rejected before open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.mp3")
		source, err := NewFileSource(path, bytes.NewReader(nil))
		if source != nil {
			t.Fatal("NewFileSource() returned a source for an unsupported extension")
		}
		var formatErr *FormatError
		if !errors.As(err, &formatErr) || !errors.Is(err, ErrUnsupportedFormat) {
			t.Fatalf("error = %v, want FormatError and ErrUnsupportedFormat", err)
		}
		if formatErr.Path != path || formatErr.Extension != ".mp3" {
			t.Fatalf("FormatError = %+v, want path and extension", formatErr)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unsupported path stat error = %v, want os.ErrNotExist", statErr)
		}
	})

	t.Run("missing path preserves path error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.raw")
		_, err := NewFileSource(path, bytes.NewReader(nil))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("error = %v, want os.ErrNotExist", err)
		}
		var streamErr *StreamError
		if !errors.As(err, &streamErr) || streamErr.Path != path || streamErr.Operation != "open" {
			t.Fatalf("error = %v, want path-aware open StreamError", err)
		}
	})

	t.Run("nil standard stream is typed", func(t *testing.T) {
		_, sourceErr := NewFileSource("-", nil)
		if !errors.Is(sourceErr, ErrNilStream) {
			t.Fatalf("source error = %v, want ErrNilStream", sourceErr)
		}
	})
}

func TestFileSourceRawFramingAndOwnership(t *testing.T) {
	samples := make([]int16, FrameSize+3)
	samples[0] = -32768
	samples[1] = 32767
	samples[len(samples)-1] = -1
	input := &trackingReader{Reader: bytes.NewReader(pcmBytes(samples))}
	source, err := NewFileSource("-", input)
	if err != nil {
		t.Fatalf("NewFileSource() error = %v", err)
	}

	first := make([]int16, FrameSize)
	if err := source.ReadFrame(context.Background(), first); err != nil {
		t.Fatalf("first ReadFrame() error = %v", err)
	}
	if !reflect.DeepEqual(first, append([]int16(nil), samples[:FrameSize]...)) {
		t.Fatalf("first frame does not preserve exact sample order")
	}

	last := make([]int16, FrameSize)
	for index := range last {
		last[index] = 99
	}
	if err := source.ReadFrame(context.Background(), last); err != nil {
		t.Fatalf("partial ReadFrame() error = %v", err)
	}
	wantLast := make([]int16, FrameSize)
	copy(wantLast, samples[FrameSize:])
	if !reflect.DeepEqual(last, wantLast) {
		t.Fatalf("partial frame = %v, want samples followed by zero padding", last)
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame after partial frame = %v, want io.EOF", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if input.closed {
		t.Fatal("source closed caller-owned stdin")
	}
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadFrame after Close() = %v, want ErrClosed", err)
	}
}

func TestFileSourceEmptyAndTruncatedRaw(t *testing.T) {
	t.Run("empty returns EOF", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.raw")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		source, err := NewFileSource(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = source.Close() }()
		if err := source.ReadFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, io.EOF) {
			t.Fatalf("ReadFrame() = %v, want io.EOF", err)
		}
	})

	t.Run("odd trailing byte is distinguishable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "odd.pcm")
		data := append(pcmBytes([]int16{7}), 0xab)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		source, err := NewFileSource(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = source.Close() }()
		readErr := source.ReadFrame(context.Background(), make([]int16, FrameSize))
		var truncErr *TruncatedPCMError
		if !errors.As(readErr, &truncErr) || !errors.Is(readErr, ErrTruncatedPCM) || truncErr.Bytes != 1 {
			t.Fatalf("ReadFrame() = %v, want one-byte TruncatedPCMError", readErr)
		}
	})
}

func TestFileSourceWAVValidationAndFraming(t *testing.T) {
	t.Run("canonical WAV and uppercase extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "input.WAV")
		samples := make([]int16, FrameSize+2)
		samples[0], samples[1], samples[2] = -32768, 32767, -1
		var encoded bytes.Buffer
		if err := wavio.Write(&encoded, SampleRate, samples); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		source, err := NewFileSource(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = source.Close() }()
		assertSourceFrames(t, source, samples)
	})

	t.Run("unsupported WAV rate is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rate.wav")
		var encoded bytes.Buffer
		if err := wavio.Write(&encoded, wavio.Rate24kHz, []int16{1}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewFileSource(path, nil)
		var formatErr *FormatError
		if !errors.As(err, &formatErr) || !errors.Is(err, wavio.ErrUnsupportedRate) {
			t.Fatalf("error = %v, want path FormatError and wavio.ErrUnsupportedRate", err)
		}
		if formatErr.Path != path || formatErr.Format != "wav" {
			t.Fatalf("FormatError = %+v, want path and wav format", formatErr)
		}
	})

	t.Run("malformed WAV preserves codec identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncated.wav")
		if err := os.WriteFile(path, []byte("RIFF\x04\x00\x00\x00WAVE"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewFileSource(path, nil)
		if !errors.Is(err, wavio.ErrMalformed) {
			t.Fatalf("error = %v, want wavio.ErrMalformed", err)
		}
		var streamErr *StreamError
		if !errors.As(err, &streamErr) || streamErr.Path != path || streamErr.Format != "wav" {
			t.Fatalf("error = %v, want path-aware WAV StreamError", err)
		}
	})
}

func TestFileSourceContextAndFrameErrors(t *testing.T) {
	reader := &countingReader{Reader: bytes.NewReader(pcmBytes([]int16{1}))}
	source, err := NewFileSource("-", reader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := source.ReadFrame(ctx, make([]int16, FrameSize)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ReadFrame() = %v, want context.Canceled", err)
	}
	if reader.reads != 0 {
		t.Fatalf("cancelled ReadFrame() performed %d reads", reader.reads)
	}

	var sizeErr *FrameSizeError
	if err := source.ReadFrame(context.Background(), make([]int16, FrameSize-1)); !errors.As(err, &sizeErr) || sizeErr.Got != FrameSize-1 {
		t.Fatalf("invalid ReadFrame() = %v, want FrameSizeError", err)
	}
	if reader.reads != 0 {
		t.Fatalf("invalid ReadFrame() performed %d reads", reader.reads)
	}
}

type trackingReader struct {
	Reader io.Reader
	closed bool
}

func (r *trackingReader) Read(destination []byte) (int, error) { return r.Reader.Read(destination) }

func (r *trackingReader) Close() error {
	r.closed = true
	return nil
}

type countingReader struct {
	Reader io.Reader
	reads  int
}

func (r *countingReader) Read(destination []byte) (int, error) {
	r.reads++
	return r.Reader.Read(destination)
}
