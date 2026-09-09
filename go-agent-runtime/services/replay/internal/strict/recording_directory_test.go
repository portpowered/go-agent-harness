package strict

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
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	publicreplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestServicePrepareRejectsTamperedRootDeclaredPCMBeforeTraceReplay(t *testing.T) {
	root := writeManifestedReplayRoot(t)
	service := New(Dependencies{ClockFactory: func(origin time.Time) *clock.Deterministic {
		return clock.NewDeterministic(origin, 10)
	}})
	if _, err := service.Prepare(context.Background(), publicreplay.Request{BundlePath: root}); err != nil {
		t.Fatalf("untouched root bundle rejected: %v", err)
	}

	pcmPath := filepath.Join(root, "audio/out-000.pcm")
	pcm, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	pcm[0] ^= 1
	if err := os.WriteFile(pcmPath, pcm, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.Prepare(context.Background(), publicreplay.Request{BundlePath: root})
	if !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("mutated root error = %v, want ErrBundleIncomplete", err)
	}
	if !strings.Contains(err.Error(), "audio/out-000.pcm") || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("mutated root error = %q, want artifact-specific digest diagnostic", err)
	}
}

func TestValidateRecordingBundleRejectsSymlinkedOrNonRegularManifest(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(string) error
		diag  string
	}{
		{
			name: "dangling symlink",
			setup: func(manifestPath string) error {
				return os.Symlink(filepath.Join(filepath.Dir(manifestPath), "missing-manifest.json"), manifestPath)
			},
			diag: "is a symlink",
		},
		{
			name:  "directory",
			setup: func(manifestPath string) error { return os.Mkdir(manifestPath, 0o700) },
			diag:  "is not a regular file",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := writeManifestedReplayRoot(t)
			manifestPath := filepath.Join(root, "manifest.json")
			if err := os.Remove(manifestPath); err != nil {
				t.Fatal(err)
			}
			if err := test.setup(manifestPath); err != nil {
				t.Fatal(err)
			}

			err := validateRecordingBundle(context.Background(), root, plan.New())
			if !errors.Is(err, publicreplay.ErrBundleIncomplete) {
				t.Fatalf("manifest error = %v, want ErrBundleIncomplete", err)
			}
			if !strings.Contains(err.Error(), "manifest.json") || !strings.Contains(err.Error(), test.diag) {
				t.Fatalf("manifest error = %q, want manifest path and %q", err, test.diag)
			}
		})
	}
}

func TestPrepareTraceDirectoryPreservesManifestlessTraceReplay(t *testing.T) {
	root := writeBundle(t, true)
	tracePath, err := prepareTraceDirectory(context.Background(), root, plan.New())
	if err != nil {
		t.Fatalf("manifestless trace rejected: %v", err)
	}
	if tracePath != root {
		t.Fatalf("trace path = %q, want %q", tracePath, root)
	}
}

func writeManifestedReplayRoot(t *testing.T) string {
	t.Helper()
	traceDirectory := writeBundle(t, true)
	root := t.TempDir()
	if err := os.Rename(traceDirectory, filepath.Join(root, "audio-trace")); err != nil {
		t.Fatal(err)
	}
	artifacts := []struct {
		path string
		data []byte
	}{
		{path: "client.transcript.jsonl", data: []byte("client\n")},
		{path: "agent.transcript.jsonl", data: []byte("agent\n")},
		{path: "provider.json", data: []byte(`{"records":[]}`)},
		{path: "audio/out-000.pcm", data: []byte{1, 2, 3, 4}},
	}
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
