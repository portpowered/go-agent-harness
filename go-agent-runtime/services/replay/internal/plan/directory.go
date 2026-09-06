package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// ResolveCapturePath accepts a raw provider capture or a finalized runtime
// recording directory. It deliberately returns the provider artifact only;
// semantic/audio evidence remains owned by the recording service and is not
// silently substituted for protocol replay.
func (*Service) ResolveCapturePath(ctx context.Context, path string) (string, error) {
	if ctx == nil {
		return "", errors.New("replay capture resolution requires a context")
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%w: capture path is empty", replay.ErrCaptureUnavailable)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: inspect capture path %s: %w", replay.ErrCaptureUnavailable, path, err)
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	if !info.IsDir() {
		if err := validateRawCapturePath(path); err != nil {
			return "", err
		}
		return path, nil
	}
	return resolveRecordingDirectory(ctx, path)
}

func resolveRecordingDirectory(ctx context.Context, directory string) (string, error) {
	manifestPath := filepath.Join(directory, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("%w: read recording manifest %s: %w", replay.ErrCaptureUnavailable, manifestPath, err)
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("%w: decode recording manifest %s: %w", replay.ErrCaptureUnavailable, manifestPath, err)
	}
	if err := manifest.Validate(); err != nil {
		return "", fmt.Errorf("%w: validate recording manifest %s: %w", replay.ErrCaptureUnavailable, manifestPath, err)
	}
	if manifest.RecordingStatus != nil && manifest.RecordingStatus.State != transcript.RecordingStatusComplete {
		return "", fmt.Errorf("%w: recording is %s", replay.ErrCaptureUnavailable, manifest.RecordingStatus.State)
	}
	artifact, ok := recordingArtifact(manifest, "provider.json")
	if !ok {
		return "", fmt.Errorf("%w: recording manifest has no provider.json artifact", replay.ErrCaptureUnavailable)
	}
	artifactPath, err := safeArtifactPath(directory, artifact.Path)
	if err != nil {
		return "", err
	}
	if err := validateArtifactPath(directory, artifactPath); err != nil {
		return "", err
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	digest, err := fileDigest(ctx, artifactPath)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return "", cause
		}
		return "", fmt.Errorf("%w: hash provider capture %s: %w", replay.ErrCaptureUnavailable, artifactPath, err)
	}
	if !strings.EqualFold(strings.TrimSpace(artifact.SHA256), digest) {
		return "", fmt.Errorf("%w: provider capture digest mismatch", replay.ErrCaptureUnavailable)
	}
	return artifactPath, nil
}

func validateRawCapturePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect capture path %s: %w", replay.ErrCaptureUnavailable, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: capture path %s is not a regular file", replay.ErrCaptureUnavailable, path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%w: capture path %s is empty", replay.ErrCaptureUnavailable, path)
	}
	return nil
}

func validateArtifactPath(directory, artifactPath string) error {
	info, err := os.Lstat(artifactPath)
	if err != nil {
		return fmt.Errorf("%w: inspect provider capture %s: %w", replay.ErrCaptureUnavailable, artifactPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%w: provider capture %s is empty, symlinked, or not a regular file", replay.ErrCaptureUnavailable, artifactPath)
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("%w: resolve recording directory: %w", replay.ErrCaptureUnavailable, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(artifactPath))
	if err != nil {
		return fmt.Errorf("%w: resolve provider artifact directory: %w", replay.ErrCaptureUnavailable, err)
	}
	realPath := filepath.Join(parent, filepath.Base(artifactPath))
	rel, err := filepath.Rel(root, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: provider artifact path resolves outside recording directory", replay.ErrCaptureUnavailable)
	}
	return nil
}

func recordingArtifact(manifest transcript.RecordingManifest, path string) (transcript.ArtifactHash, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == path {
			return artifact, true
		}
	}
	return transcript.ArtifactHash{}, false
}

func safeArtifactPath(directory, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("%w: provider artifact path is unsafe", replay.ErrCaptureUnavailable)
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("%w: resolve recording directory: %w", replay.ErrCaptureUnavailable, err)
	}
	candidate, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("%w: resolve provider artifact: %w", replay.ErrCaptureUnavailable, err)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: provider artifact path escapes recording directory", replay.ErrCaptureUnavailable)
	}
	return candidate, nil
}

func fileDigest(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, contextReader{ctx: ctx, reader: file})
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(buffer)
	if cause := context.Cause(r.ctx); cause != nil {
		return n, cause
	}
	return n, err
}
