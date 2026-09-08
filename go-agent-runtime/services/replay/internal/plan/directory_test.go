package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

func TestResolveCapturePathAcceptsRawCapture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	if err := os.WriteFile(path, []byte(`{"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := New().ResolveCapturePath(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != path {
		t.Fatalf("resolved raw capture = %q, want %q", resolved, path)
	}
}

func TestResolveCapturePathValidatesCanonicalDirectoryArtifact(t *testing.T) {
	directory := t.TempDir()
	provider := []byte(`{"version":1,"records":[]}`)
	client := []byte("client transcript\n")
	agent := []byte("agent transcript\n")
	providerPath := filepath.Join(directory, "provider.json")
	if err := os.WriteFile(providerPath, provider, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "client.transcript.jsonl"), client, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "agent.transcript.jsonl"), agent, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(provider)
	clientDigest := sha256.Sum256(client)
	agentDigest := sha256.Sum256(agent)
	manifest := transcript.RecordingManifest{
		FormatVersion:   transcript.RecordingManifestVersion,
		RecordingStatus: &transcript.RecordingStatus{State: transcript.RecordingStatusComplete},
		Artifacts: []transcript.ArtifactHash{
			{Path: "client.transcript.jsonl", SHA256: hex.EncodeToString(clientDigest[:])},
			{Path: "agent.transcript.jsonl", SHA256: hex.EncodeToString(agentDigest[:])},
			{Path: "provider.json", SHA256: hex.EncodeToString(digest[:])},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := New().ResolveCapturePath(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != providerPath {
		t.Fatalf("resolved directory capture = %q, want %q", resolved, providerPath)
	}
}

func TestResolveCapturePathRejectsPartialOrTamperedDirectory(t *testing.T) {
	directory := t.TempDir()
	providerPath := filepath.Join(directory, "provider.json")
	if err := os.WriteFile(providerPath, []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := transcript.RecordingManifest{
		FormatVersion:   transcript.RecordingManifestVersion,
		RecordingStatus: &transcript.RecordingStatus{State: transcript.RecordingStatusPartial, Reason: "provider capture unavailable"},
		Artifacts: []transcript.ArtifactHash{
			{Path: "client.transcript.jsonl", SHA256: strings.Repeat("1", 64)},
			{Path: "agent.transcript.jsonl", SHA256: strings.Repeat("2", 64)},
			{Path: "provider.json", SHA256: strings.Repeat("a", 64)},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New().ResolveCapturePath(t.Context(), directory); !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
		t.Fatalf("partial directory error = %v, want ErrCaptureUnavailable", err)
	}

	manifest.RecordingStatus = &transcript.RecordingStatus{State: transcript.RecordingStatusComplete}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New().ResolveCapturePath(t.Context(), directory); !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
		t.Fatalf("tampered directory error = %v, want ErrCaptureUnavailable", err)
	}
}

func TestResolveCapturePathRejectsSymlinkedOrNonRegularProviderArtifact(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{
		{
			name: "symlink",
			setup: func(directory string) error {
				outside := filepath.Join(t.TempDir(), "provider.json")
				if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
					return err
				}
				return os.Symlink(outside, filepath.Join(directory, "provider.json"))
			},
		},
		{
			name: "directory",
			setup: func(directory string) error {
				return os.Mkdir(filepath.Join(directory, "provider.json"), 0o700)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := test.setup(directory); err != nil {
				t.Fatal(err)
			}
			manifest := transcript.RecordingManifest{
				FormatVersion:   transcript.RecordingManifestVersion,
				RecordingStatus: &transcript.RecordingStatus{State: transcript.RecordingStatusComplete},
				Artifacts: []transcript.ArtifactHash{
					{Path: "client.transcript.jsonl", SHA256: strings.Repeat("1", 64)},
					{Path: "agent.transcript.jsonl", SHA256: strings.Repeat("2", 64)},
					{Path: "provider.json", SHA256: strings.Repeat("a", 64)},
				},
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := New().ResolveCapturePath(t.Context(), directory); !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
				t.Fatalf("artifact admission error = %v, want ErrCaptureUnavailable", err)
			}
		})
	}

	rawDirectory := t.TempDir()
	if _, err := New().ResolveCapturePath(t.Context(), rawDirectory); !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
		t.Fatalf("raw directory admission error = %v, want ErrCaptureUnavailable", err)
	}
	rawTarget := filepath.Join(t.TempDir(), "provider.json")
	if err := os.WriteFile(rawTarget, []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawLink := filepath.Join(t.TempDir(), "provider-link.json")
	if err := os.Symlink(rawTarget, rawLink); err != nil {
		t.Fatal(err)
	}
	if _, err := New().ResolveCapturePath(t.Context(), rawLink); !errors.Is(err, publicreplay.ErrCaptureUnavailable) {
		t.Fatalf("raw symlink admission error = %v, want ErrCaptureUnavailable", err)
	}
}

func TestResolveCapturePathPreservesCancellation(t *testing.T) {
	cause := errors.New("stop replay admission")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)
	if _, err := New().ResolveCapturePath(ctx, filepath.Join(t.TempDir(), "capture")); !errors.Is(err, cause) {
		t.Fatalf("cancellation error = %v, want %v", err, cause)
	}
}
