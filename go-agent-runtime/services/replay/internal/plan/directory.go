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
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

const recordingDigestBufferSize = 32 * 1024

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
	if ctx == nil {
		return "", errors.New("replay recording directory resolution requires a context")
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	directory = strings.TrimSpace(directory)
	if err := validateRecordingDirectory(directory); err != nil {
		return "", err
	}
	manifest, err := loadRecordingManifest(ctx, directory)
	if err != nil {
		return "", err
	}
	if manifest.RecordingStatus != nil && manifest.RecordingStatus.State != transcript.RecordingStatusComplete {
		return "", fmt.Errorf("%w: recording is %s", replay.ErrCaptureUnavailable, manifest.RecordingStatus.State)
	}
	provider, ok := recordingArtifact(manifest, "provider.json")
	if !ok {
		return "", fmt.Errorf("%w: recording manifest has no provider.json artifact", replay.ErrCaptureUnavailable)
	}
	return validateRecordingArtifacts(ctx, directory, manifest, provider)
}

func validateRecordingDirectory(directory string) error {
	if directory == "" {
		return fmt.Errorf("%w: recording directory is empty", replay.ErrCaptureUnavailable)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("%w: inspect recording directory %s: %w", replay.ErrCaptureUnavailable, directory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: recording path %s is not a directory", replay.ErrCaptureUnavailable, directory)
	}
	return nil
}

func loadRecordingManifest(ctx context.Context, directory string) (transcript.RecordingManifest, error) {
	manifestPath := filepath.Join(directory, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return transcript.RecordingManifest{}, fmt.Errorf("%w: inspect recording manifest %s: %w", replay.ErrCaptureUnavailable, manifestPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return transcript.RecordingManifest{}, fmt.Errorf("%w: recording manifest %s is a symlink", replay.ErrCaptureUnavailable, manifestPath)
	}
	if !info.Mode().IsRegular() {
		return transcript.RecordingManifest{}, fmt.Errorf("%w: recording manifest %s is not a regular file", replay.ErrCaptureUnavailable, manifestPath)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return transcript.RecordingManifest{}, fmt.Errorf("%w: read recording manifest %s: %w", replay.ErrCaptureUnavailable, manifestPath, err)
	}
	if err := context.Cause(ctx); err != nil {
		return transcript.RecordingManifest{}, err
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return transcript.RecordingManifest{}, recordingManifestError("decode", manifestPath, data, err)
	}
	if err := manifest.Validate(); err != nil {
		return transcript.RecordingManifest{}, recordingManifestError("validate", manifestPath, data, err)
	}
	return manifest, nil
}

func validateRecordingArtifacts(ctx context.Context, directory string, manifest transcript.RecordingManifest, provider transcript.ArtifactHash) (string, error) {
	var providerPath string
	for _, artifact := range manifest.Artifacts {
		artifactPath, err := validateRecordingArtifact(ctx, directory, artifact, artifact.Path == provider.Path)
		if err != nil {
			return "", err
		}
		if artifact.Path == provider.Path {
			providerPath = artifactPath
		}
	}
	return providerPath, nil
}

func validateRecordingArtifact(ctx context.Context, directory string, artifact transcript.ArtifactHash, requireNonEmpty bool) (string, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	artifactPath, err := safeRecordingArtifactPath(directory, artifact.Path)
	if err != nil {
		return "", fmt.Errorf("%w: recording artifact %q: %w", replay.ErrCaptureUnavailable, artifact.Path, err)
	}
	if err := validateRecordingArtifactPath(directory, artifactPath, artifact.Path, requireNonEmpty); err != nil {
		return "", err
	}
	digest, err := fileDigest(ctx, artifactPath)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return "", cause
		}
		return "", fmt.Errorf("%w: hash recording artifact %q: %w", replay.ErrCaptureUnavailable, artifact.Path, err)
	}
	if strings.EqualFold(strings.TrimSpace(artifact.SHA256), digest) {
		return artifactPath, nil
	}
	return "", fmt.Errorf(
		"%w: recording artifact %q digest mismatch (expected %s, actual %s)",
		replay.ErrCaptureUnavailable, artifact.Path, artifact.SHA256, digest,
	)
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

func recordingArtifact(manifest transcript.RecordingManifest, path string) (transcript.ArtifactHash, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == path {
			return artifact, true
		}
	}
	return transcript.ArtifactHash{}, false
}

func recordingManifestError(operation, path string, data []byte, cause error) error {
	if artifactPath := recordingManifestArtifactHint(data, cause.Error()); artifactPath != "" {
		return fmt.Errorf(
			"%w: %s recording manifest %s (artifact %q): %w",
			replay.ErrCaptureUnavailable, operation, path, artifactPath, cause,
		)
	}
	return fmt.Errorf("%w: %s recording manifest %s: %w", replay.ErrCaptureUnavailable, operation, path, cause)
}

func recordingManifestArtifactHint(data []byte, message string) string {
	var hint struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if json.Unmarshal(data, &hint) != nil {
		return ""
	}
	if index := recordingManifestArtifactIndex(message); index >= 0 && index < len(hint.Artifacts) {
		return hint.Artifacts[index].Path
	}
	if len(hint.Artifacts) > 0 {
		return hint.Artifacts[0].Path
	}
	return ""
}

func recordingManifestArtifactIndex(message string) int {
	marker := strings.Index(message, "artifacts[")
	if marker < 0 {
		return -1
	}
	start := marker + len("artifacts[")
	end := strings.IndexByte(message[start:], ']')
	if end < 0 {
		return -1
	}
	index, err := strconv.Atoi(message[start : start+end])
	if err != nil {
		return -1
	}
	return index
}

func safeRecordingArtifactPath(directory, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("artifact path is unsafe")
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve recording directory: %w", err)
	}
	candidate, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("resolve artifact: %w", err)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes recording directory")
	}
	return candidate, nil
}

func validateRecordingArtifactPath(directory, artifactPath, declaredPath string, requireNonEmpty bool) error {
	info, err := os.Lstat(artifactPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: recording artifact %q is missing", replay.ErrCaptureUnavailable, declaredPath)
		}
		return fmt.Errorf("%w: inspect recording artifact %q: %w", replay.ErrCaptureUnavailable, declaredPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: recording artifact %q is a symlink", replay.ErrCaptureUnavailable, declaredPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: recording artifact %q is not a regular file", replay.ErrCaptureUnavailable, declaredPath)
	}
	if requireNonEmpty && info.Size() == 0 {
		return fmt.Errorf("%w: recording artifact %q is empty", replay.ErrCaptureUnavailable, declaredPath)
	}

	root, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("%w: resolve recording directory for artifact %q: %w", replay.ErrCaptureUnavailable, declaredPath, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: resolve recording directory for artifact %q: %w", replay.ErrCaptureUnavailable, declaredPath, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(artifactPath))
	if err != nil {
		return fmt.Errorf("%w: resolve parent for recording artifact %q: %w", replay.ErrCaptureUnavailable, declaredPath, err)
	}
	realPath := filepath.Join(parent, filepath.Base(artifactPath))
	rel, err := filepath.Rel(root, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: recording artifact %q resolves outside recording directory", replay.ErrCaptureUnavailable, declaredPath)
	}
	return nil
}

func fileDigest(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, copyErr := io.CopyBuffer(digest, contextReader{ctx: ctx, reader: file}, make([]byte, recordingDigestBufferSize))
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
