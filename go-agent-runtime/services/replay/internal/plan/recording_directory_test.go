package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

func TestResolveRecordingDirectoryValidatesEveryDeclaredArtifactAndAllowsEmptyNonProvider(t *testing.T) {
	root := writeManifestedBundle(t, []recordingTestArtifact{
		{path: "client.transcript.jsonl", data: []byte("client\n")},
		{path: "agent.transcript.jsonl", data: []byte("agent\n")},
		{path: "provider.json", data: []byte(`{"records":[]}`)},
		{path: "audio/out-000.pcm", data: []byte{1, 2, 3, 4}},
		{path: "audio/empty.pcm", data: nil},
	})

	resolved, err := resolveRecordingDirectory(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Join(root, "provider.json") {
		t.Fatalf("resolved provider = %q, want %q", resolved, filepath.Join(root, "provider.json"))
	}
}

func TestResolveRecordingDirectoryRejectsMutatedDeclaredPCMWithArtifactDiagnostic(t *testing.T) {
	root := writeManifestedBundle(t, []recordingTestArtifact{
		{path: "client.transcript.jsonl", data: []byte("client\n")},
		{path: "agent.transcript.jsonl", data: []byte("agent\n")},
		{path: "provider.json", data: []byte(`{"records":[]}`)},
		{path: "audio/out-000.pcm", data: []byte{1, 2, 3, 4}},
	})
	pcmPath := filepath.Join(root, "audio/out-000.pcm")
	pcm, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	pcm[0] ^= 1
	if err := os.WriteFile(pcmPath, pcm, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = resolveRecordingDirectory(t.Context(), root)
	if !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
		t.Fatalf("mutated PCM error = %v, want ErrCaptureUnavailable", err)
	}
	for _, expected := range []string{"audio/out-000.pcm", "digest mismatch", "expected", "actual"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("mutated PCM error = %q, want %q", err, expected)
		}
	}
}

func TestResolveRecordingDirectoryRejectsMissingTruncatedSymlinkAndNonRegularArtifacts(t *testing.T) {
	for _, name := range []string{"missing", "truncated", "symlink", "non-regular"} {
		root := writeManifestedBundle(t, []recordingTestArtifact{
			{path: "client.transcript.jsonl", data: []byte("client\n")},
			{path: "agent.transcript.jsonl", data: []byte("agent\n")},
			{path: "provider.json", data: []byte(`{"records":[]}`)},
			{path: "audio/out-000.pcm", data: []byte{1, 2, 3, 4}},
		})
		if err := mutateRecordingArtifact(t, root, name); err != nil {
			t.Fatal(err)
		}
		_, err := resolveRecordingDirectory(t.Context(), root)
		if !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
			t.Fatalf("%s artifact error = %v, want ErrCaptureUnavailable", name, err)
		}
		if !strings.Contains(err.Error(), "audio/out-000.pcm") || !strings.Contains(err.Error(), recordingArtifactFailure(name)) {
			t.Fatalf("%s artifact error = %q, want path and diagnostic", name, err)
		}
	}
}

func mutateRecordingArtifact(t *testing.T, root, name string) error {
	t.Helper()
	path := filepath.Join(root, "audio/out-000.pcm")
	switch name {
	case "missing":
		return os.Remove(path)
	case "truncated":
		return os.Truncate(path, 1)
	case "symlink":
		if err := os.Remove(path); err != nil {
			return err
		}
		outside := filepath.Join(t.TempDir(), "outside.pcm")
		if err := os.WriteFile(outside, []byte{1, 2, 3, 4}, 0o600); err != nil {
			return err
		}
		return os.Symlink(outside, path)
	case "non-regular":
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Mkdir(path, 0o700)
	default:
		return errors.New("unknown recording artifact mutation")
	}
}

func recordingArtifactFailure(name string) string {
	if name == "missing" {
		return "is missing"
	}
	if name == "truncated" {
		return "digest mismatch"
	}
	if name == "symlink" {
		return "is a symlink"
	}
	return "not a regular file"
}

func TestResolveRecordingDirectoryRejectsUnsafeManifestArtifactPath(t *testing.T) {
	root := t.TempDir()
	manifest := map[string]any{
		"format_version":   1,
		"recording_status": map[string]string{"state": transcript.RecordingStatusComplete},
		"artifacts": []map[string]string{
			{"path": "client.transcript.jsonl", "sha256": strings.Repeat("1", 64)},
			{"path": "agent.transcript.jsonl", "sha256": strings.Repeat("2", 64)},
			{"path": "../audio/out-000.pcm", "sha256": strings.Repeat("3", 64)},
			{"path": "provider.json", "sha256": strings.Repeat("4", 64)},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = resolveRecordingDirectory(t.Context(), root)
	if !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
		t.Fatalf("unsafe artifact error = %v, want ErrCaptureUnavailable", err)
	}
	if !strings.Contains(err.Error(), "../audio/out-000.pcm") {
		t.Fatalf("unsafe artifact error = %q, want declared path", err)
	}
}

func TestRecordingDirectoryHashReaderPreservesCancellation(t *testing.T) {
	cause := errors.New("stop while hashing")
	ctx, cancel := context.WithCancelCause(t.Context())
	reader := &cancelingRecordingReader{cancel: func() { cancel(cause) }}
	wrapped := contextReader{ctx: ctx, reader: reader}
	buffer := make([]byte, 16)
	n, err := wrapped.Read(buffer)
	if n != len("capture") || !errors.Is(err, cause) {
		t.Fatalf("context reader = (%d, %v), want read and cancellation cause", n, err)
	}
	if _, err := io.Copy(io.Discard, wrapped); !errors.Is(err, cause) {
		t.Fatalf("second context reader read = %v, want cancellation cause", err)
	}
}

type recordingTestArtifact struct {
	path string
	data []byte
}

func writeManifestedBundle(t *testing.T, artifacts []recordingTestArtifact) string {
	t.Helper()
	root := t.TempDir()
	hashes := make([]transcript.ArtifactHash, 0, len(artifacts))
	for _, artifact := range artifacts {
		path := filepath.Join(root, filepath.FromSlash(artifact.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifact.data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(artifact.data)
		hashes = append(hashes, transcript.ArtifactHash{Path: artifact.path, SHA256: hex.EncodeToString(digest[:])})
	}
	manifest := transcript.RecordingManifest{
		FormatVersion:   transcript.RecordingManifestVersion,
		RecordingStatus: &transcript.RecordingStatus{State: transcript.RecordingStatusComplete},
		Artifacts:       hashes,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

type cancelingRecordingReader struct {
	cancel func()
	done   bool
}

func (r *cancelingRecordingReader) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	copy(buffer, "capture")
	r.cancel()
	return len("capture"), nil
}
