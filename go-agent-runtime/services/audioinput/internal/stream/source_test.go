package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestValidateInputRejectsConflictsBeforeSourceUse(t *testing.T) {
	called := false
	input := Input{
		Source: audioSourceFunc(func(context.Context, []int16) error {
			called = true
			return nil
		}),
		Reader: bytes.NewReader(nil),
	}
	if err := ValidateInput(input); !errors.Is(err, ErrConflict) {
		t.Fatalf("ValidateInput error = %v, want ErrConflict", err)
	}
	if called {
		t.Fatal("validation read from a conflicting source")
	}
}

func TestNewBufferSourceCopiesAndReadPCMPreservesShortTail(t *testing.T) {
	pcm := pcm16Bytes(11, -22, 33)
	source, err := NewBufferSource(pcm)
	if err != nil {
		t.Fatalf("NewBufferSource: %v", err)
	}
	pcm[0] = 0
	pcm[1] = 0
	got, rate, err := ReadPCM(context.Background(), source, 16000)
	if err != nil {
		t.Fatalf("ReadPCM: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("rate = %d, want 16000", rate)
	}
	if want := pcm16Bytes(11, -22, 33); !bytes.Equal(got, want) {
		t.Fatalf("PCM = %v, want %v", got, want)
	}
}

func TestNewWAVSourcePreservesUnsupportedFormatCause(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.wav")
	if err := os.WriteFile(path, []byte("not a wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close WAV fixture: %v", err)
		}
	}()
	_, err = NewWAVSource(path, file)
	if !errors.Is(err, ErrFormat) || !errors.Is(err, audio.ErrUnsupportedFormat) {
		t.Fatalf("NewWAVSource error = %v, want runtime and audio format identities", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindFormat || typed.Path != path {
		t.Fatalf("typed error = %#v, want format path %q", typed, path)
	}
}

func TestReaderSourceClosePolicyAndExactOnce(t *testing.T) {
	reader := &countingReader{reader: bytes.NewReader(pcm16Bytes(1, 2))}
	source, err := NewReaderSource(reader, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := reader.closes.Load(); got != 1 {
		t.Fatalf("reader close count = %d, want 1", got)
	}

	callerOwned := &countingReader{reader: bytes.NewReader(nil)}
	ownedSource, err := NewReaderSource(callerOwned, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ownedSource.Close(); err != nil {
		t.Fatalf("caller-owned Close: %v", err)
	}
	if got := callerOwned.closes.Load(); got != 0 {
		t.Fatalf("caller-owned reader close count = %d, want 0", got)
	}
}

func TestReaderSourceRejectsUninterruptibleReaderWithCause(t *testing.T) {
	reader := uninterruptibleReader{}
	source, err := NewReaderSource(reader, false)
	if err != nil {
		t.Fatal(err)
	}
	err = source.ReadFrame(context.Background(), make([]int16, audio.FrameSize))
	if !errors.Is(err, ErrUninterruptible) {
		t.Fatalf("ReadFrame error = %v, want ErrUninterruptible", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("uninterruptible reader fabricated cancellation")
	}
}

func TestReaderSourceDeadlineSetupPreservesCause(t *testing.T) {
	deadlineErr := errors.New("deadline setup failed")
	reader := &testDeadlineReader{err: deadlineErr}
	source, err := NewReaderSource(reader, false)
	if err != nil {
		t.Fatal(err)
	}
	err = source.ReadFrame(context.Background(), make([]int16, audio.FrameSize))
	if !errors.Is(err, ErrUninterruptible) || !errors.Is(err, deadlineErr) {
		t.Fatalf("ReadFrame error = %v, want interruptibility and setup causes", err)
	}
}

func pcm16Bytes(samples ...int16) []byte {
	result := make([]byte, len(samples)*2)
	for index, sample := range samples {
		result[index*2] = byte(sample)
		result[index*2+1] = byte(uint16(sample) >> 8)
	}
	return result
}

type audioSourceFunc func(context.Context, []int16) error

func (f audioSourceFunc) ReadFrame(ctx context.Context, destination []int16) error {
	return f(ctx, destination)
}

func (audioSourceFunc) Close() error { return nil }

type countingReader struct {
	reader io.Reader
	closes atomic.Int32
}

func (r *countingReader) Read(destination []byte) (int, error) { return r.reader.Read(destination) }
func (r *countingReader) Close() error {
	r.closes.Add(1)
	return nil
}

type uninterruptibleReader struct{}

func (uninterruptibleReader) Read([]byte) (int, error) { return 0, io.EOF }

type testDeadlineReader struct{ err error }

func (r *testDeadlineReader) Read([]byte) (int, error)        { return 0, io.EOF }
func (r *testDeadlineReader) SetReadDeadline(time.Time) error { return r.err }
