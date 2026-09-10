package audio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
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
func TestFileSourceToFileSinkRawRoundTrip(t *testing.T) {
	samples := make([]int16, FrameSize*2)
	for index := range samples {
		samples[index] = int16(index*5 - 2000)
	}
	samples[0], samples[1], samples[2] = -32768, 32767, -1
	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "input.raw")
	outputPath := filepath.Join(tempDir, "output.pcm")
	want := pcmBytes(samples)
	if err := os.WriteFile(inputPath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewFileSource(inputPath, nil)
	if err != nil {
		t.Fatalf("NewFileSource() error = %v", err)
	}
	sink, err := NewFileSink(outputPath, nil)
	if err != nil {
		_ = source.Close()
		t.Fatalf("NewFileSink() error = %v", err)
	}
	for {
		frame := make([]int16, FrameSize)
		err := source.ReadFrame(context.Background(), frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = source.Close()
			_ = sink.Close()
			t.Fatalf("ReadFrame() error = %v", err)
		}
		if err := sink.WriteFrame(context.Background(), frame); err != nil {
			_ = source.Close()
			_ = sink.Close()
			t.Fatalf("WriteFrame() error = %v", err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatalf("source Close() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("sink Close() error = %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("raw source-to-sink bytes = %v, want exact input bytes", got)
	}
}
func TestFileSinkOwnedHandleRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.raw")
	before := processOpenHandleCount(t)
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatalf("NewFileSink() error = %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	opened := processOpenHandleCount(t)
	if opened <= before {
		t.Fatalf("open-handle count after sink open = %d, before = %d; owned handle was not observed", opened, before)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	afterFirst := settledProcessOpenHandleCount(t, before)
	assertHandleCountWithinTolerance(t, afterFirst, before, "sink first close")
	if afterFirst >= opened {
		t.Fatalf("open-handle count after sink first close = %d, opened = %d; owned handle was not released", afterFirst, opened)
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	afterSecond := settledProcessOpenHandleCount(t, afterFirst)
	if afterSecond != afterFirst {
		t.Fatalf("open-handle count after sink second close = %d, first close = %d; idempotent close changed the count", afterSecond, afterFirst)
	}
	if err := sink.WriteFrame(context.Background(), make([]int16, FrameSize)); !errors.Is(err, ErrClosed) {
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

func TestFileSinkWAVUsesRequestedSampleRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.wav")
	first, last := []int16{-32768, 0}, []int16{32767}
	sink, err := NewFileSinkAtSampleRate(path, nil, 24000)
	if err != nil {
		t.Fatalf("NewFileSinkAtSampleRate() error = %v", err)
	}
	if err := sink.WriteSamples(context.Background(), first); err != nil {
		if closeErr := sink.Close(); closeErr != nil {
			t.Fatalf("WriteSamples() error = %v; Close() error = %v", err, closeErr)
		}
		t.Fatalf("WriteSamples() error = %v", err)
	}
	if gotRate, got := readSinkWAV(t, path); gotRate != 24000 || !reflect.DeepEqual(got, first) {
		t.Fatalf("checkpoint rate/samples = %d/%v, want 24000/%v", gotRate, got, first)
	}
	if err := sink.WriteSamples(context.Background(), last); err != nil {
		t.Fatalf("second WriteSamples() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}
	want := append(append([]int16{}, first...), last...)
	if gotRate, got := readSinkWAV(t, path); gotRate != 24000 || !reflect.DeepEqual(got, want) {
		t.Fatalf("WAV rate/samples = %d/%v, want 24000/%v", gotRate, got, want)
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
	max       int
	err       error
	calls     int
	failAfter bool
	cancel    context.CancelFunc
}

func (w *shortWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.failAfter && w.calls > 1 {
		return 0, w.err
	}
	if len(data) > w.max {
		data = data[:w.max]
	}
	written, err := w.Buffer.Write(data)
	if w.cancel != nil {
		w.cancel()
	}
	if w.err != nil && !w.failAfter {
		return written, w.err
	}
	return written, err
}

type errorWriter struct{ err error }

func (w *errorWriter) Write([]byte) (int, error) { return 0, w.err }

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type invalidCountWriter struct {
	delta int
	err   error
}

func (w invalidCountWriter) Write(data []byte) (int, error) {
	delta := w.delta
	if delta == 0 {
		delta = 1
	}
	return len(data) + delta, w.err
}

type closeError struct{ err error }

func (c closeError) Close() error { return c.err }

const boundedSinkChunkSamples = rawSinkScratchBytes / 2

func TestFileSinkBoundedRawFailureControls(t *testing.T) {
	partialErr, chunkErr := errors.New("partial writer failed"), errors.New("second chunk failed")
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shortSamples := []int16{-32768, -1, 0, 1, 32767, 12, 34}
	largeSamples := repeatSinkSamples(boundedSinkChunkSamples+1, 7)
	for _, test := range []struct {
		name          string
		samples       []int16
		writer        io.Writer
		want, reject  error
		prefix, calls int
		stream        bool
	}{
		{"partial error", shortSamples, &shortWriter{max: 5, err: partialErr}, partialErr, nil, 5, 1, true},
		{"later chunk", largeSamples, &shortWriter{max: rawSinkScratchBytes, err: chunkErr, failAfter: true}, chunkErr, nil, rawSinkScratchBytes, 2, false},
		{"zero count", shortSamples, zeroWriter{}, io.ErrShortWrite, nil, 0, 0, false},
		{"negative count", shortSamples, invalidCountWriter{delta: -1}, io.ErrShortWrite, nil, 0, 0, false},
		{"too-large count", shortSamples, invalidCountWriter{delta: 1}, io.ErrShortWrite, nil, 0, 0, false},
		{"invalid count precedes error", shortSamples, invalidCountWriter{delta: 1, err: partialErr}, io.ErrShortWrite, partialErr, 0, 0, false},
		{"cancellation", largeSamples, &shortWriter{max: rawSinkScratchBytes, cancel: cancel}, context.Canceled, nil, rawSinkScratchBytes, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := newRawSink(test.writer).WriteSamples(cancelCtx, test.samples)
			if !errors.Is(err, test.want) || (test.reject != nil && errors.Is(err, test.reject)) {
				t.Fatalf("WriteSamples() = %v, want %v without %v", err, test.want, test.reject)
			}
			if test.stream {
				var streamErr *StreamError
				if !errors.As(err, &streamErr) || streamErr.Operation != "write" {
					t.Fatalf("error = %v, want write StreamError", err)
				}
			}
			if test.prefix > 0 {
				assertWriterPrefix(t, test.writer.(interface{ Bytes() []byte }), test.samples, test.prefix)
			}
			if test.calls > 0 {
				assertWriterCalls(t, test.writer, test.calls)
			}
		})
	}
}
func TestFileSinkBoundedRawLiteralTailsAndChunks(t *testing.T) {
	tail := repeatSinkSamples(boundedSinkChunkSamples+8, 0)
	for index, sample := range []int16{-32768, -1, 0, 1, 32767} {
		for offset := index; offset < len(tail); offset += 5 {
			tail[offset] = sample
		}
	}
	for _, test := range []struct {
		name string
		want []int16
	}{
		{"empty", nil}, {"one sample", []int16{-32768}},
		{"exact chunk", repeatSinkSamples(boundedSinkChunkSamples, 7)}, {"short tail", tail},
	} {
		t.Run(test.name, func(t *testing.T) { assertBoundedRawBytes(t, test.want) })
	}
}
func assertBoundedRawBytes(t *testing.T, samples []int16) {
	t.Helper()
	writer := &bytes.Buffer{}
	sink, err := NewFileSink("-", writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteSamples(context.Background(), samples); err != nil {
		t.Fatalf("WriteSamples() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	assertWriterBytes(t, writer, samples)
}
func TestFileSinkBoundedRawDeadlineIdentity(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	writer := &bytes.Buffer{}
	if err := newRawSink(writer).WriteSamples(ctx, []int16{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WriteSamples() = %v, want context.DeadlineExceeded", err)
	}
	if writer.Len() != 0 {
		t.Fatal("deadline-canceled write emitted output")
	}
}
func TestFileSinkBoundedRawSerializationAndClose(t *testing.T) {
	writer, sink := newGateSinkWriter(), (*FileSink)(nil)
	first, second := repeatSinkSamples(boundedSinkChunkSamples+1, 1), repeatSinkSamples(FrameSize, 2)
	sink = newRawSink(writer)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { firstDone <- sink.WriteSamples(context.Background(), first) }()
	<-writer.entered
	go func() { secondDone <- sink.WriteFrame(context.Background(), second) }()
	close(writer.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if got, want := writer.Bytes(), append(pcmBytes(first), pcmBytes(second)...); !bytes.Equal(got, want) {
		t.Fatal("concurrent WriteSamples/WriteFrame calls interleaved")
	}

	writer, sink = newGateSinkWriter(), nil
	sink = newRawSink(writer)
	writeDone, closeDone, closeStarted := make(chan error, 1), make(chan error, 1), make(chan struct{})
	go func() { writeDone <- sink.WriteSamples(context.Background(), []int16{3}) }()
	<-writer.entered
	go func() { close(closeStarted); closeDone <- sink.Close() }()
	<-closeStarted
	select {
	case <-closeDone:
		t.Fatal("Close completed while WriteSamples was blocked")
	default:
	}
	close(writer.release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteSamples(context.Background(), []int16{4}); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after Close() = %v, want ErrClosed", err)
	}
}
func readSinkWAV(t *testing.T, path string) (int, []int16) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return rate, samples
}
func newRawSink(writer io.Writer) *FileSink {
	return &FileSink{path: "-", format: formatRaw, writer: writer}
}
func repeatSinkSamples(count int, value int16) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = value
	}
	return samples
}
func assertWriterBytes(t *testing.T, writer interface{ Bytes() []byte }, samples []int16) {
	t.Helper()
	if got, want := writer.Bytes(), pcmBytes(samples); !bytes.Equal(got, want) {
		t.Fatalf("writer bytes = %x, want %x", got, want)
	}
}
func assertWriterPrefix(t *testing.T, writer interface{ Bytes() []byte }, samples []int16, byteCount int) {
	t.Helper()
	want := pcmBytes(samples)
	if got := writer.Bytes(); !bytes.Equal(got, want[:byteCount]) {
		t.Fatalf("writer bytes = %x, want prefix %x", got, want[:byteCount])
	}
}

func assertWriterCalls(t *testing.T, writer io.Writer, want int) {
	t.Helper()
	got := writer.(*shortWriter).calls
	if got != want {
		t.Fatalf("writer calls = %d, want %d", got, want)
	}
}

type gateSinkWriter struct {
	bytes.Buffer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGateSinkWriter() *gateSinkWriter {
	return &gateSinkWriter{entered: make(chan struct{}), release: make(chan struct{})}
}
func (w *gateSinkWriter) Write(payload []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.Buffer.Write(payload)
}
