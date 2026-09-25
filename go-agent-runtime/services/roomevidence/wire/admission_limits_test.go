package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/admission"
)

func TestServiceRejectsOversizedReplayManifest(t *testing.T) {
	t.Parallel()
	bundle := t.TempDir()
	path := filepath.Join(bundle, roomevidence.ManifestPath)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if err := file.Truncate(admission.MaxManifestBytes + 1); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close manifest after truncate failure: %v", closeErr)
		}
		t.Fatalf("make oversized manifest: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close manifest: %v", err)
	}

	_, err = newTestService().LoadPlan(bundle)
	assertAdmissionLimitError(t, err, "manifest")
}

// Small admission limits keep the timeline boundary fixtures in kilobytes; the
// production defaults are asserted in the admission package and by the
// oversized-file tests below.
const (
	testTimelineBytes  = 16 << 10
	testTimelineEvents = 64
)

func newLimitedTestService() roomevidence.Service {
	return NewServiceWithOptions(roomevidence.ServiceOptions{
		SyncFile:        skipFileSync,
		AdmissionLimits: roomevidence.AdmissionLimits{TimelineBytes: testTimelineBytes, TimelineEvents: testTimelineEvents},
	})
}

func TestServiceBoundsReplayTimelineBytesAndEventCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		events int
		size   int
		reject string
	}{
		{name: "total bytes", size: testTimelineBytes + 1, reject: "byte limit"},
		{name: "bytes at limit", events: 1, size: testTimelineBytes},
		{name: "event count", events: testTimelineEvents + 1, reject: "event limit"},
		{name: "events at limit", events: testTimelineEvents},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundle, _ := finalizedReplayBundle(t)
			rewriteReplayTimeline(t, bundle, func(dst io.Writer) error { return writeTestTimeline(dst, tc.events, tc.size) })

			plan, err := newLimitedTestService().LoadPlan(bundle)
			if tc.reject == "" {
				if err != nil || len(plan.Timeline) != tc.events {
					t.Fatalf("at-limit timeline: events=%d err=%v, want %d admitted", len(plan.Timeline), err, tc.events)
				}
				return
			}
			assertAdmissionLimitError(t, err, "room_timeline")
			if !strings.Contains(err.Error(), tc.reject) {
				t.Fatalf("timeline error = %v, want %s", err, tc.reject)
			}
		})
	}
}

// writeTestTimeline writes events valid timeline records, then pads with
// whitespace-only lines (which admission skips) to exactly size bytes.
func writeTestTimeline(dst io.Writer, events, size int) error {
	var data bytes.Buffer
	for sequence := 0; sequence < events; sequence++ {
		fmt.Fprintf(&data, `{"sequence":%d,"monotonic_offset_ms":0,"participant_id":"speaker","event":"test"}`+"\n", sequence)
	}
	for data.Len() < size {
		line := min(size-data.Len(), 1<<10)
		data.Write(bytes.Repeat([]byte(" "), line-1))
		data.WriteByte('\n')
	}
	_, err := dst.Write(data.Bytes())
	return err
}

func TestServiceRejectsOversizedReplayFilesBeforeHashing(t *testing.T) {
	t.Parallel()
	t.Run("timeline", func(t *testing.T) {
		t.Parallel()
		bundle, _ := finalizedReplayBundle(t)
		rewriteOversizedReplayFile(t, bundle, roomevidence.TimelinePath, admission.MaxTimelineBytes+1)

		_, err := newTestService().LoadPlan(bundle)
		assertAdmissionLimitError(t, err, "room_timeline")
	})
	t.Run("artifact", func(t *testing.T) {
		t.Parallel()
		bundle, recorder := finalizedReplayBundle(t)
		artifact := recorder.Artifacts("speaker").SentPCM
		rewriteOversizedReplayFile(t, bundle, artifact, admission.MaxArtifactBytes+1)

		_, err := newTestService().LoadPlan(bundle)
		assertAdmissionLimitError(t, err, "participant:speaker:sent_pcm")
	})
}

func rewriteOversizedReplayFile(t *testing.T, bundle, relative string, size int64) {
	t.Helper()
	path := filepath.Join(bundle, filepath.FromSlash(relative))
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open replay artifact: %v", err)
	}
	truncateErr := file.Truncate(size)
	closeErr := file.Close()
	if truncateErr != nil {
		t.Fatalf("make oversized replay artifact: %v (close: %v)", truncateErr, closeErr)
	}
	if closeErr != nil {
		t.Fatalf("close oversized replay artifact: %v", closeErr)
	}
	rewriteReplayArtifactIntegrity(t, bundle, relative, size, strings.Repeat("0", sha256.Size*2))
}

func assertAdmissionLimitError(t *testing.T, err error, field string) {
	t.Helper()
	var bundleErr *roomevidence.BundleError
	if err == nil || !errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) || !errors.As(err, &bundleErr) {
		t.Fatalf("admission error = %v, want typed invalid bundle error", err)
	}
	if bundleErr.Kind != roomevidence.BundleMismatch || bundleErr.Field != field {
		t.Fatalf("admission error = %+v, want mismatch in %q", bundleErr, field)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("admission error = %v, want limit diagnostic", err)
	}
}

func rewriteReplayTimeline(t *testing.T, bundle string, write func(io.Writer) error) {
	t.Helper()
	timelinePath := filepath.Join(bundle, roomevidence.TimelinePath)
	file, err := os.Create(timelinePath)
	if err != nil {
		t.Fatalf("open timeline: %v", err)
	}
	writeErr := write(file)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatalf("write timeline: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close timeline: %v", closeErr)
	}
	info, err := os.Stat(timelinePath)
	if err != nil {
		t.Fatalf("stat timeline: %v", err)
	}
	hash, err := hashFile(timelinePath)
	if err != nil {
		t.Fatalf("hash timeline: %v", err)
	}
	rewriteReplayArtifactIntegrity(t, bundle, roomevidence.TimelinePath, info.Size(), hash)
}

func rewriteReplayArtifactIntegrity(t *testing.T, bundle, relative string, size int64, hash string) {
	t.Helper()
	manifestPath := filepath.Join(bundle, roomevidence.ManifestPath)
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	type integrityEntry struct {
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	var integrity map[string]integrityEntry
	if err := json.Unmarshal(manifest["artifact_integrity"], &integrity); err != nil {
		t.Fatalf("decode artifact integrity: %v", err)
	}
	integrity[relative] = integrityEntry{Size: size, SHA256: hash}
	updatedIntegrity, err := json.Marshal(integrity)
	if err != nil {
		t.Fatalf("encode artifact integrity: %v", err)
	}
	manifest["artifact_integrity"] = updatedIntegrity
	updatedManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, updatedManifest, 0o600); err != nil {
		t.Fatalf("write updated manifest: %v", err)
	}
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
