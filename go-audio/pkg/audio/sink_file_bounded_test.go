package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const boundedSinkChunkSamples = rawSinkScratchBytes / 2

func TestFileSinkBoundedRawLiteralTailsAndChunks(t *testing.T) {
	writer := &bytes.Buffer{}
	sink, err := NewFileSink("-", writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.WriteSamples(context.Background(), nil); err != nil {
		t.Fatalf("empty WriteSamples() error = %v", err)
	}
	samples := make([]int16, boundedSinkChunkSamples+8)
	literal := []int16{-32768, -1, 0, 1, 32767}
	for index := range samples {
		samples[index] = literal[index%len(literal)]
	}
	if err := sink.WriteSamples(context.Background(), samples); err != nil {
		t.Fatalf("chunked WriteSamples() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	want := literalPCM16(samples)
	if !bytes.Equal(writer.Bytes(), want) {
		t.Fatalf("raw bytes = %x, want %x", writer.Bytes(), want)
	}
	if len(writer.Bytes()) != (boundedSinkChunkSamples+8)*2 {
		t.Fatalf("raw byte count = %d, want %d", writer.Len(), (boundedSinkChunkSamples+8)*2)
	}
}

func TestFileSinkBoundedRawWriteFailures(t *testing.T) {
	samples := []int16{-32768, -1, 0, 1, 32767, 12, 34}
	want := literalPCM16(samples)

	t.Run("odd short writes complete", func(t *testing.T) {
		writer := &boundedShortWriter{max: 3}
		sink := &FileSink{path: "-", format: formatRaw, writer: writer}
		if err := sink.WriteSamples(context.Background(), samples); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(writer.Bytes(), want) {
			t.Fatalf("short-write bytes = %x, want %x", writer.Bytes(), want)
		}
	})

	t.Run("zero and invalid counts preserve short write", func(t *testing.T) {
		for name, writer := range map[string]io.Writer{
			"zero":      boundedZeroWriter{},
			"negative":  boundedInvalidCountWriter{delta: -1},
			"too-large": boundedInvalidCountWriter{delta: 1},
		} {
			t.Run(name, func(t *testing.T) {
				sink := &FileSink{path: "-", format: formatRaw, writer: writer}
				if !errors.Is(sink.WriteSamples(context.Background(), samples), io.ErrShortWrite) {
					t.Fatalf("WriteSamples() did not preserve io.ErrShortWrite")
				}
			})
		}
	})

	t.Run("partial bytes and sentinel error are preserved", func(t *testing.T) {
		wantErr := errors.New("bounded writer failed")
		writer := &boundedPartialErrorWriter{max: 5, err: wantErr}
		sink := &FileSink{path: "-", format: formatRaw, writer: writer}
		err := sink.WriteSamples(context.Background(), samples)
		if !errors.Is(err, wantErr) {
			t.Fatalf("WriteSamples() = %v, want sentinel", err)
		}
		var streamErr *StreamError
		if !errors.As(err, &streamErr) || streamErr.Operation != "write" {
			t.Fatalf("error = %v, want write StreamError", err)
		}
		if !bytes.Equal(writer.Bytes(), want[:5]) {
			t.Fatalf("partial bytes = %x, want %x", writer.Bytes(), want[:5])
		}
	})

	t.Run("failure after an accepted chunk stops at exact prefix", func(t *testing.T) {
		wantErr := errors.New("second chunk failed")
		writer := &boundedFailAfterWriter{err: wantErr}
		samples := repeatSamples(boundedSinkChunkSamples+1, 7)
		sink := &FileSink{path: "-", format: formatRaw, writer: writer}
		if err := sink.WriteSamples(context.Background(), samples); !errors.Is(err, wantErr) {
			t.Fatalf("WriteSamples() = %v, want sentinel", err)
		}
		wantPrefix := literalPCM16(samples[:boundedSinkChunkSamples])
		if !bytes.Equal(writer.Bytes(), wantPrefix) {
			t.Fatalf("prefix bytes = %d, want %d", writer.Len(), len(wantPrefix))
		}
		if writer.calls != 2 {
			t.Fatalf("writer calls = %d, want one accepted chunk and one failing chunk", writer.calls)
		}
	})

	t.Run("invalid count wins over writer error", func(t *testing.T) {
		wantErr := errors.New("should not escape invalid count")
		sink := &FileSink{path: "-", format: formatRaw, writer: boundedInvalidCountWriter{delta: 1, err: wantErr}}
		err := sink.WriteSamples(context.Background(), samples)
		if !errors.Is(err, io.ErrShortWrite) || errors.Is(err, wantErr) {
			t.Fatalf("WriteSamples() = %v, want only io.ErrShortWrite", err)
		}
	})
}

func TestFileSinkBoundedRawCancellationBetweenChunks(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writer := &boundedCancelWriter{cancel: cancel}
		samples := repeatSamples(boundedSinkChunkSamples+1, 9)
		sink := &FileSink{path: "-", format: formatRaw, writer: writer}
		err := sink.WriteSamples(ctx, samples)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteSamples() = %v, want context.Canceled", err)
		}
		want := literalPCM16(samples[:boundedSinkChunkSamples])
		if !bytes.Equal(writer.Bytes(), want) {
			t.Fatalf("canceled prefix bytes = %d, want %d", writer.Len(), len(want))
		}
		if writer.calls != 1 {
			t.Fatalf("writer calls = %d, want one accepted chunk", writer.calls)
		}
	})

	t.Run("deadline identity", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), timePast())
		defer cancel()
		writer := &bytes.Buffer{}
		sink := &FileSink{path: "-", format: formatRaw, writer: writer}
		if err := sink.WriteSamples(ctx, []int16{1}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WriteSamples() = %v, want context.DeadlineExceeded", err)
		}
		if writer.Len() != 0 {
			t.Fatal("deadline-canceled write emitted output")
		}
	})
}

func TestFileSinkBoundedRawSerializationAndClose(t *testing.T) {
	writer := newBoundedGateWriter()
	sink := &FileSink{path: "-", format: formatRaw, writer: writer}
	first := repeatSamples(boundedSinkChunkSamples+1, 1)
	second := repeatSamples(5, 2)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- sink.WriteSamples(context.Background(), first) }()
	<-writer.entered
	go func() { secondDone <- sink.WriteSamples(context.Background(), second) }()
	close(writer.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	want := append(literalPCM16(first), literalPCM16(second)...)
	if !bytes.Equal(writer.Bytes(), want) {
		t.Fatal("concurrent WriteSamples calls interleaved")
	}

	gate := newBoundedGateWriter()
	closedSink := &FileSink{path: "-", format: formatRaw, writer: gate}
	writeDone := make(chan error, 1)
	go func() { writeDone <- closedSink.WriteSamples(context.Background(), []int16{3}) }()
	<-gate.entered
	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- closedSink.Close()
	}()
	<-closeStarted
	select {
	case <-closeDone:
		t.Fatal("Close completed while WriteSamples was blocked")
	default:
	}
	close(gate.release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := closedSink.WriteSamples(context.Background(), []int16{4}); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after waiting Close() = %v, want ErrClosed", err)
	}
}

func TestFileSinkBoundedWAVCheckpointAndCloseIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.wav")
	sink, err := NewFileSink(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := []int16{-32768, -1, 0}
	if err := sink.WriteSamples(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := wavio.Read(bytes.NewReader(data))
	if err != nil || !bytes.Equal(literalPCM16(got), literalPCM16(first)) {
		t.Fatalf("checkpoint read got samples=%v err=%v", got, err)
	}
	if err := sink.WriteSamples(context.Background(), []int16{32767, 123}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("repeated Close() = %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err = wavio.Read(bytes.NewReader(data))
	want := append(append([]int16{}, first...), 32767, 123)
	if err != nil || !bytes.Equal(literalPCM16(got), literalPCM16(want)) {
		t.Fatalf("final WAV samples = %v err=%v, want %v", got, err, want)
	}

	emptyPath := filepath.Join(t.TempDir(), "empty.wav")
	empty, err := NewFileSink(emptyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstClose := empty.Close()
	secondClose := empty.Close()
	if !errors.Is(firstClose, wavio.ErrEmptySamples) || !errors.Is(secondClose, wavio.ErrEmptySamples) || firstClose.Error() != secondClose.Error() {
		t.Fatalf("empty close errors = %v, %v; want stable ErrEmptySamples", firstClose, secondClose)
	}
}

func literalPCM16(samples []int16) []byte {
	encoded := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
	}
	return encoded
}

func repeatSamples(count int, value int16) []int16 {
	samples := make([]int16, count)
	for index := range samples {
		samples[index] = value
	}
	return samples
}

func timePast() time.Time { return time.Now().Add(-time.Second) }

type boundedShortWriter struct {
	bytes.Buffer
	max int
}

func (w *boundedShortWriter) Write(payload []byte) (int, error) {
	if len(payload) > w.max {
		payload = payload[:w.max]
	}
	return w.Buffer.Write(payload)
}

type boundedZeroWriter struct{}

func (boundedZeroWriter) Write([]byte) (int, error) { return 0, nil }

type boundedInvalidCountWriter struct {
	delta int
	err   error
}

func (w boundedInvalidCountWriter) Write(payload []byte) (int, error) {
	return len(payload) + w.delta, w.err
}

type boundedPartialErrorWriter struct {
	bytes.Buffer
	max int
	err error
}

func (w *boundedPartialErrorWriter) Write(payload []byte) (int, error) {
	if len(payload) > w.max {
		payload = payload[:w.max]
	}
	_, _ = w.Buffer.Write(payload)
	return len(payload), w.err
}

type boundedFailAfterWriter struct {
	bytes.Buffer
	calls int
	err   error
}

func (w *boundedFailAfterWriter) Write(payload []byte) (int, error) {
	w.calls++
	if w.calls > 1 {
		return 0, w.err
	}
	return w.Buffer.Write(payload)
}

type boundedCancelWriter struct {
	bytes.Buffer
	calls  int
	cancel context.CancelFunc
}

func (w *boundedCancelWriter) Write(payload []byte) (int, error) {
	w.calls++
	_, _ = w.Buffer.Write(payload)
	w.cancel()
	return len(payload), nil
}

type boundedGateWriter struct {
	bytes.Buffer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBoundedGateWriter() *boundedGateWriter {
	return &boundedGateWriter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *boundedGateWriter) Write(payload []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.Buffer.Write(payload)
}
