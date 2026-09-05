package wavio

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestStreamWriterCheckpointAndExactTail(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "audio.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := NewStreamWriter(f, Rate24kHz)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int16, 40001)
	for i := range samples {
		samples[i] = int16(i * 31)
	}
	for _, span := range [][2]int{{0, 1}, {1, 39999}, {39999, 40001}} {
		if err := w.WriteSamples(samples[span[0]:span[1]]); err != nil {
			t.Fatal(err)
		}
		if err := w.Checkpoint(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		rate, got, err := Read(bytes.NewReader(data))
		if err != nil || rate != Rate24kHz || !slices.Equal(got, samples[:span[1]]) {
			t.Fatalf("checkpoint %d rate=%d samples=%d err=%v", span[1], rate, len(got), err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSamples([]int16{1}); err == nil {
		t.Fatal("write after close accepted")
	}
	if w.BytesWritten() != uint64(len(samples)*2) {
		t.Fatal(w.BytesWritten())
	}
	data, _ := os.ReadFile(f.Name())
	var want bytes.Buffer
	if err := Write(&want, Rate24kHz, samples); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want.Bytes()) {
		t.Fatal("streaming and whole-file encoders differ")
	}
}

type failingSeekWriter struct {
	file *os.File
	fail bool
}

func (w *failingSeekWriter) Write(p []byte) (int, error) {
	if w.fail {
		return 0, io.ErrShortWrite
	}
	return w.file.Write(p)
}
func (w *failingSeekWriter) Seek(offset int64, whence int) (int64, error) {
	return w.file.Seek(offset, whence)
}
func TestStreamWriterRetainsFirstFailure(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "failure.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dst := &failingSeekWriter{file: f}
	w, err := NewStreamWriter(dst, Rate16kHz)
	if err != nil {
		t.Fatal(err)
	}
	dst.fail = true
	if err := w.WriteSamples([]int16{1}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	dst.fail = false
	if err := w.Close(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if w.BytesWritten() != 0 {
		t.Fatal(w.BytesWritten())
	}
}
