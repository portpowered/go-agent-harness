package selfplay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestWriteAllHandlesPartialWritesAndRejectsNoProgress(t *testing.T) {
	var output bytes.Buffer
	writer := partialWriter{target: &output, max: 2}
	n, err := WriteAll(writer, []byte("abcdef"))
	if err != nil || n != 6 || output.String() != "abcdef" {
		t.Fatalf("WriteAll = (%d, %v), output %q", n, err, output.String())
	}
	_, err = WriteAll(zeroWriter{}, []byte("x"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer error = %v", err)
	}
	if _, err := WriteAll(nil, []byte("x")); err == nil {
		t.Fatal("nil writer should be rejected")
	}
}

func TestEvidenceWritersEnforceFiniteBudgets(t *testing.T) {
	destination := t.TempDir()
	jsonl, err := NewJSONLWriter(filepath.Join(destination, "events.jsonl"), EvidenceLimits{JSONLBytes: 4, JSONLItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonl.Write(map[string]string{"x": "too-long"}); !errors.Is(err, ErrEvidenceQuota) {
		t.Fatalf("JSONL quota error = %v", err)
	}
	_ = jsonl.Close()
	wav, err := NewWAVWriter(filepath.Join(destination, "audio.wav"), EvidenceSampleRate, EvidenceLimits{WAVBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := wav.Write(context.Background(), []byte{1, 2, 3, 4}); !errors.Is(err, ErrEvidenceQuota) {
		t.Fatalf("WAV quota error = %v", err)
	}
	if err := wav.Write(context.Background(), []byte{1}); err == nil {
		t.Fatal("odd PCM should be rejected")
	}
	_ = wav.Close()
}

func TestRedactErrorRemovesCredentialValues(t *testing.T) {
	got := RedactError("authorization: Bearer secret-token; api_key=other", "secret-token")
	if got == "" || bytes.Contains([]byte(got), []byte("secret-token")) || bytes.Contains([]byte(got), []byte("other")) {
		t.Fatalf("redacted error = %q", got)
	}
}

type partialWriter struct {
	target io.Writer
	max    int
}

func (w partialWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.target.Write(p)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
