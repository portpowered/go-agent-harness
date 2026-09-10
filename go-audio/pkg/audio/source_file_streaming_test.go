package audio

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestFileSourceWAVUsesOneStreamingCursorForMixedReads(t *testing.T) {
	samples := make([]int16, FrameSize+3)
	for index := range samples {
		samples[index] = int16(index - 200)
	}
	samples[0], samples[1], samples[2], samples[len(samples)-1] = -32768, -12345, 12345, 32767
	path := writeStreamingWAV(t, SampleRate, samples)

	source, err := NewFileSource(path, nil)
	if err != nil {
		t.Fatalf("NewFileSource() error = %v", err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("source.Close() error = %v", err)
		}
	})

	frame := make([]int16, FrameSize)
	if err := source.ReadFrame(context.Background(), frame); err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if !reflect.DeepEqual(frame, samples[:FrameSize]) {
		t.Fatalf("frame = %v, want first %d samples", frame[:4], FrameSize)
	}
	tail := make([]int16, 3)
	count, err := source.ReadSamples(context.Background(), tail)
	if err != nil || count != len(tail) || !reflect.DeepEqual(tail, samples[FrameSize:]) {
		t.Fatalf("ReadSamples() = %d, %v, %v; want exact tail %v", count, err, tail, samples[FrameSize:])
	}
	for attempt := 0; attempt < 2; attempt++ {
		count, err = source.ReadSamples(context.Background(), tail)
		if count != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("ReadSamples() after tail attempt %d = %d, %v; want repeated EOF", attempt, count, err)
		}
	}
}

func TestFileSourceWAVReadsPayloadAfterOpenWithoutSnapshot(t *testing.T) {
	path := writeStreamingWAV(t, SampleRate, []int16{11, 22})
	source, err := NewFileSource(path, nil)
	if err != nil {
		t.Fatalf("NewFileSource() error = %v", err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("source.Close() error = %v", err)
		}
	})

	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var replacement [2]byte
	// The canonical 44-byte header puts the first data sample at byte 44.
	replacement[0], replacement[1] = 0x41, 0x01 // 321 in little-endian PCM16.
	if _, writeErr := file.WriteAt(replacement[:], 44); writeErr != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("WriteAt() error = %v; file.Close() error = %v", writeErr, closeErr)
		}
		t.Fatal(writeErr)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got := make([]int16, 1)
	if count, err := source.ReadSamples(context.Background(), got); count != 1 || err != nil || got[0] != 321 {
		t.Fatalf("ReadSamples() = %d, %v, %v; want rewritten sample 321", count, err, got)
	}
}

func TestFileSourceWAVRejectsPhysicalTruncationBeforeFirstRead(t *testing.T) {
	path := writeStreamingWAV(t, SampleRate, []int16{1, 2, 3})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-1], 0o600); err != nil {
		t.Fatal(err)
	}

	source, err := NewFileSource(path, nil)
	if source != nil {
		if closeErr := source.Close(); closeErr != nil {
			t.Fatalf("unexpected source.Close() error = %v", closeErr)
		}
		t.Fatal("NewFileSource() returned a source for a physically truncated WAV")
	}
	if !errors.Is(err, wavio.ErrTruncated) {
		t.Fatalf("NewFileSource() error = %v, want wavio.ErrTruncated", err)
	}
	var streamErr *StreamError
	if !errors.As(err, &streamErr) || streamErr.Operation != "read" || streamErr.Path != path || streamErr.Format != "wav" {
		t.Fatalf("NewFileSource() error = %v, want path-aware WAV StreamError", err)
	}
}

func TestWAVSourceMetadataAndPayloadReadBounds(t *testing.T) {
	encoded, err := encodedStreamingWAV(SampleRate, []int16{-32768, -12345, -1, 0, 1, 12345, 32767})
	if err != nil {
		t.Fatal(err)
	}
	reader := newStreamingCountingReadSeekCloser(encoded)
	source, err := NewWAVSource("counted.wav", reader)
	if err != nil {
		t.Fatalf("NewWAVSource() error = %v", err)
	}
	if openPayload := reader.payloadReadBytes(44); openPayload != 0 || reader.readBytes() > 64 {
		t.Fatalf("open reads = %d bytes, %d payload bytes; want <=64 and no payload", reader.readBytes(), openPayload)
	}
	before := len(reader.reads)
	got := make([]int16, 7)
	if count, err := source.ReadSamples(context.Background(), got); count != 7 || err != nil || !reflect.DeepEqual(got, []int16{-32768, -12345, -1, 0, 1, 12345, 32767}) {
		if closeErr := source.Close(); closeErr != nil {
			t.Fatalf("ReadSamples() = %d, %v, %v; source.Close() error = %v", count, err, got, closeErr)
		}
		t.Fatalf("ReadSamples() = %d, %v, %v", count, err, got)
	}
	if readBytes := reader.readBytesSince(before); readBytes != 14 || reader.payloadReadBytesSince(before, 44) != 14 {
		if closeErr := source.Close(); closeErr != nil {
			t.Fatalf("ReadSamples() read %d payload bytes out of %d total; source.Close() error = %v", reader.payloadReadBytesSince(before, 44), readBytes, closeErr)
		}
		t.Fatalf("ReadSamples() read %d payload bytes out of %d total; want exactly 14", reader.payloadReadBytesSince(before, 44), readBytes)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if reader.closeCalls != 1 {
		t.Fatalf("close calls = %d, want one", reader.closeCalls)
	}
}

func TestWAVSourcePreservesCancellationIOAndCloseErrorIdentity(t *testing.T) {
	encoded, err := encodedStreamingWAV(SampleRate, []int16{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	reader := newStreamingCountingReadSeekCloser(encoded)
	source, err := NewWAVSource("cancel.wav", reader)
	if err != nil {
		t.Fatal(err)
	}
	before := reader.readBytes()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	count, readErr := source.ReadSamples(ctx, make([]int16, 1))
	if count != 0 || !errors.Is(readErr, context.Canceled) || reader.readBytes() != before {
		if closeErr := source.Close(); closeErr != nil {
			t.Fatalf("cancelled read = %d, %v, reads before/after=%d/%d; source.Close() error = %v", count, readErr, before, reader.readBytes(), closeErr)
		}
		t.Fatalf("cancelled read = %d, %v, reads before/after=%d/%d", count, readErr, before, reader.readBytes())
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("close failed")
	closeReader := newStreamingCountingReadSeekCloser(encoded)
	closeReader.closeErr = wantErr
	closeSource, err := NewWAVSource("close.wav", closeReader)
	if err != nil {
		t.Fatal(err)
	}
	firstErr := closeSource.Close()
	secondErr := closeSource.Close()
	if !errors.Is(firstErr, wantErr) || !errors.Is(secondErr, wantErr) || closeReader.closeCalls != 1 {
		t.Fatalf("close errors/calls = %v, %v, %d; want same wrapped error and one close", firstErr, secondErr, closeReader.closeCalls)
	}
}

func TestWAVSourcePreservesPayloadStreamErrorAndClosedPrecedence(t *testing.T) {
	encoded, err := encodedStreamingWAV(SampleRate, []int16{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("payload read failed")
	reader := newStreamingCountingReadSeekCloser(encoded)
	source, err := NewWAVSource("error.wav", reader)
	if err != nil {
		t.Fatal(err)
	}
	reader.failPayload = wantErr
	count, readErr := source.ReadSamples(context.Background(), make([]int16, 1))
	if count != 0 || !errors.Is(readErr, wantErr) {
		if closeErr := source.Close(); closeErr != nil {
			t.Fatalf("payload read = %d, %v; source.Close() error = %v", count, readErr, closeErr)
		}
		t.Fatalf("payload read = %d, %v; want wrapped payload error", count, readErr)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadSamples(context.Background(), make([]int16, 1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close = %v, want ErrClosed", err)
	}
}

func writeStreamingWAV(t *testing.T, rate int, samples []int16) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.wav")
	encoded, err := encodedStreamingWAV(rate, samples)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encodedStreamingWAV(rate int, samples []int16) ([]byte, error) {
	header, err := wavio.PCM16Header(rate, uint64(len(samples)*2))
	if err != nil {
		return nil, err
	}
	return append(header[:], pcmBytes(samples)...), nil
}

type streamingCountingReadSeekCloser struct {
	data        []byte
	position    int64
	reads       []readSpan
	closeCalls  int
	closeErr    error
	failPayload error
}

type readSpan struct {
	start int64
	end   int64
}

func newStreamingCountingReadSeekCloser(data []byte) *streamingCountingReadSeekCloser {
	return &streamingCountingReadSeekCloser{data: data}
}

func (r *streamingCountingReadSeekCloser) Read(destination []byte) (int, error) {
	start := r.position
	if r.failPayload != nil && start >= 44 {
		r.reads = append(r.reads, readSpan{start: start, end: start})
		return 0, r.failPayload
	}
	if start >= int64(len(r.data)) {
		r.reads = append(r.reads, readSpan{start: start, end: start})
		return 0, io.EOF
	}
	count := copy(destination, r.data[start:])
	r.position += int64(count)
	r.reads = append(r.reads, readSpan{start: start, end: r.position})
	if count < len(destination) {
		return count, io.EOF
	}
	return count, nil
}

func (r *streamingCountingReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	var position int64
	switch whence {
	case io.SeekStart:
		position = offset
	case io.SeekCurrent:
		position = r.position + offset
	case io.SeekEnd:
		position = int64(len(r.data)) + offset
	default:
		return r.position, errors.New("invalid seek whence")
	}
	if position < 0 {
		return r.position, errors.New("negative seek")
	}
	r.position = position
	return position, nil
}

func (r *streamingCountingReadSeekCloser) Close() error {
	r.closeCalls++
	return r.closeErr
}

func (r *streamingCountingReadSeekCloser) readBytes() int {
	return r.readBytesSince(0)
}

func (r *streamingCountingReadSeekCloser) readBytesSince(index int) int {
	total := 0
	for _, span := range r.reads[index:] {
		total += int(span.end - span.start)
	}
	return total
}

func (r *streamingCountingReadSeekCloser) payloadReadBytes(payloadStart int64) int {
	return r.payloadReadBytesSince(0, payloadStart)
}

func (r *streamingCountingReadSeekCloser) payloadReadBytesSince(index int, payloadStart int64) int {
	total := int64(0)
	for _, span := range r.reads[index:] {
		start, end := span.start, span.end
		if start < payloadStart {
			start = payloadStart
		}
		if end > int64(len(r.data)) {
			end = int64(len(r.data))
		}
		if end > start {
			total += end - start
		}
	}
	return int(total)
}
