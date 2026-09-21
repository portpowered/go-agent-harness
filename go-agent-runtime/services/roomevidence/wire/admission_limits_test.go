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
	bundle := t.TempDir()
	path := filepath.Join(bundle, roomevidence.ManifestPath)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	if err := file.Truncate(admission.MaxManifestBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("make oversized manifest: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close manifest: %v", err)
	}

	_, err = NewService().LoadPlan(bundle)
	assertAdmissionLimitError(t, err, "manifest")
}

func TestServiceBoundsReplayTimelineBytesAndEventCount(t *testing.T) {
	t.Run("total bytes", func(t *testing.T) {
		bundle, _ := finalizedReplayBundle(t)
		rewriteReplayTimeline(t, bundle, writeOversizedWhitespaceTimeline)

		_, err := NewService().LoadPlan(bundle)
		assertAdmissionLimitError(t, err, "room_timeline")
		if !strings.Contains(err.Error(), "byte limit") {
			t.Fatalf("timeline error = %v, want total byte limit", err)
		}
	})

	t.Run("event count", func(t *testing.T) {
		bundle, _ := finalizedReplayBundle(t)
		rewriteReplayTimeline(t, bundle, writeOversizedEventTimeline)

		_, err := NewService().LoadPlan(bundle)
		assertAdmissionLimitError(t, err, "room_timeline")
		if !strings.Contains(err.Error(), "event limit") {
			t.Fatalf("timeline error = %v, want event count limit", err)
		}
	})
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

func writeOversizedWhitespaceTimeline(dst io.Writer) error {
	spaces := bytes.Repeat([]byte(" "), 64<<10)
	// Thirty-five scanner-sized whitespace records exceed the total byte cap
	// without reaching the event-count cap or allocating the whole file.
	for line := 0; line < 35; line++ {
		for chunk := 0; chunk < 15; chunk++ {
			if _, err := dst.Write(spaces); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(dst, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeOversizedEventTimeline(dst io.Writer) error {
	for sequence := 0; sequence <= admission.MaxTimelineEvents; sequence++ {
		if _, err := fmt.Fprintf(dst, `{"sequence":%d,"monotonic_offset_ms":0,"participant_id":"speaker","event":"test"}`+"\n", sequence); err != nil {
			return err
		}
	}
	return nil
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
	integrity[roomevidence.TimelinePath] = integrityEntry{Size: info.Size(), SHA256: hash}
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
