package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

func TestEvidenceFactoryWriteAllHandlesPartialWritesAndRejectsNoProgress(t *testing.T) {
	factory := NewEvidenceFactory()
	var output bytes.Buffer
	writer := partialEvidenceWriter{target: &output, max: 2}
	n, err := factory.WriteAll(writer, []byte("abcdef"))
	if err != nil || n != 6 || output.String() != "abcdef" {
		t.Fatalf("WriteAll = (%d, %v), output %q", n, err, output.String())
	}
	_, err = factory.WriteAll(zeroEvidenceWriter{}, []byte("x"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer error = %v", err)
	}
	if _, err := factory.WriteAll(nil, []byte("x")); err == nil {
		t.Fatal("nil writer should be rejected")
	}
}

func TestEvidenceFactoryWritersEnforceFiniteBudgets(t *testing.T) {
	factory := NewEvidenceFactory()
	destination := t.TempDir()
	jsonl, err := factory.NewJSONLWriter(filepath.Join(destination, "events.jsonl"), selfplay.EvidenceLimits{JSONLBytes: 4, JSONLItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonl.Write(map[string]string{"x": "too-long"}); !errors.Is(err, selfplay.ErrEvidenceQuota) {
		t.Fatalf("JSONL quota error = %v", err)
	}
	if closeErr := jsonl.Close(); closeErr != nil && !errors.Is(closeErr, selfplay.ErrEvidenceQuota) {
		t.Fatalf("JSONL close: %v", closeErr)
	}
	wav, err := factory.NewWAVWriter(filepath.Join(destination, "audio.wav"), selfplay.EvidenceSampleRate, selfplay.EvidenceLimits{WAVBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if wav.Path() != filepath.Join(destination, "audio.wav") || wav.DataBytes() != 0 {
		t.Fatalf("WAV metadata = path:%q bytes:%d", wav.Path(), wav.DataBytes())
	}
	if err := wav.Write(context.Background(), []byte{1, 2, 3, 4}); !errors.Is(err, selfplay.ErrEvidenceQuota) {
		t.Fatalf("WAV quota error = %v", err)
	}
	if err := wav.Write(context.Background(), []byte{1}); err == nil {
		t.Fatal("odd PCM should be rejected")
	}
	if closeErr := wav.Close(); closeErr != nil && !errors.Is(closeErr, selfplay.ErrEvidenceQuota) {
		t.Fatalf("WAV close: %v", closeErr)
	}
	clean, err := factory.NewWAVWriter(filepath.Join(destination, "closed-audio.wav"), selfplay.EvidenceSampleRate, selfplay.EvidenceLimits{WAVBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := clean.Close(); err != nil {
		t.Fatal(err)
	}
	if err := clean.Write(context.Background(), []byte{1, 2}); !errors.Is(err, selfplay.ErrEvidenceClosed) {
		t.Fatalf("closed WAV write error = %v", err)
	}
}

func TestEvidenceFactoryWrapJSONLWriterUsesProvidedWriterAndCloses(t *testing.T) {
	factory := NewEvidenceFactory()
	var output bytes.Buffer
	writer, err := factory.WrapJSONLWriter("memory.jsonl", &output, selfplay.EvidenceLimits{JSONLBytes: 64, JSONLItems: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(map[string]string{"event": "ready"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("wrapped writer did not receive JSONL output")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteRaw([]byte(`{"event":"late"}`)); !errors.Is(err, selfplay.ErrEvidenceClosed) {
		t.Fatalf("closed wrapped writer error = %v", err)
	}
	if _, err := factory.WrapJSONLWriter("memory.jsonl", nil, selfplay.EvidenceLimits{}); err == nil {
		t.Fatal("nil wrapped writer was accepted")
	}
}

func TestEvidenceFactoryRedactErrorRemovesCredentialValues(t *testing.T) {
	factory := NewEvidenceFactory()
	got := factory.RedactError("authorization: Bearer secret-token; api_key=other", "secret-token")
	if got == "" || bytes.Contains([]byte(got), []byte("secret-token")) || bytes.Contains([]byte(got), []byte("other")) {
		t.Fatalf("redacted error = %q", got)
	}
}

type partialEvidenceWriter struct {
	target io.Writer
	max    int
}

func (w partialEvidenceWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.target.Write(p)
}

type zeroEvidenceWriter struct{}

func (zeroEvidenceWriter) Write([]byte) (int, error) { return 0, nil }
