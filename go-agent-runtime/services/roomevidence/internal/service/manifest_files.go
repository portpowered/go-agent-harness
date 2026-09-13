package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

func (r *recorder) hashArtifactInto(integrity map[string]artifactIntegrity, relative string) {
	if strings.TrimSpace(relative) == "" {
		return
	}
	path := filepath.Join(r.destination, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	hash, err := hashFile(path)
	if err != nil {
		return
	}
	integrity[filepath.ToSlash(relative)] = artifactIntegrity{Size: info.Size(), SHA256: hash}
}

func hashFile(path string) (hash string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeManifestFile(path string, manifest roomManifest, secrets []string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room run manifest: %w", err)
	}
	data = append(redactJSON(data, secrets), '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create room run manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := writeManifestTemporary(temporary, data); err != nil {
		return errors.Join(fmt.Errorf("write room run manifest temporary file: %w", err), removeManifestTemporary(temporaryPath))
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.Join(fmt.Errorf("replace room run manifest: %w", err), removeManifestTemporary(temporaryPath))
	}
	return nil
}

func writeManifestTemporary(file *os.File, data []byte) (err error) {
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close room run manifest temporary file: %w", closeErr))
		}
	}()
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

func removeManifestTemporary(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove room run manifest temporary file: %w", err)
	}
	return nil
}

func (r *recorder) cleanupOpenFiles() {
	if r.timeline != nil {
		r.recordCleanupError("", roomevidence.TimelinePath, r.timeline.close())
		r.removeCleanupFile(roomevidence.TimelinePath)
	}
	for _, participant := range r.participants {
		if participant == nil {
			continue
		}
		r.recordCleanupError(participant.id, participant.artifacts.WAV, participant.wav.close())
		r.recordCleanupError(participant.id, participant.artifacts.Diagnostics, participant.diagnostics.close())
		r.recordCleanupError(participant.id, participant.artifacts.Deltas, participant.deltas.close())
		r.recordCleanupError(participant.id, participant.artifacts.Events, participant.events.close())
		r.recordCleanupError(participant.id, participant.artifacts.SentPCM, participant.sentPCM.close())
		r.recordCleanupError(participant.id, participant.artifacts.ReceivedPCM, participant.receivedPCM.close())
		for _, path := range []string{participant.artifacts.WAV, participant.artifacts.Diagnostics, participant.artifacts.Deltas, participant.artifacts.Events, participant.artifacts.SentPCM, participant.artifacts.ReceivedPCM} {
			r.removeCleanupFile(path)
		}
	}
}

func (r *recorder) recordCleanupError(participant, artifact string, err error) {
	if err != nil {
		r.recordError(participant, artifact, err)
	}
}

func (r *recorder) removeCleanupFile(relative string) {
	path := filepath.Join(r.destination, filepath.FromSlash(relative))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.recordError("", relative, err)
	}
}
