package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

func validateArtifacts(root string, refs []artifactRef) ([]rooms.RoomReplayArtifact, error) {
	seen := make(map[string]string, len(refs))
	result := make([]rooms.RoomReplayArtifact, 0, len(refs))
	for _, ref := range refs {
		artifact, err := validateArtifact(root, seen, ref)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	return result, nil
}

func validateArtifact(root string, seen map[string]string, ref artifactRef) (rooms.RoomReplayArtifact, error) {
	artifact := ref.artifact
	relative, absolute, err := safePath(root, artifact.Path)
	if err != nil {
		return rooms.RoomReplayArtifact{}, mismatch(ref.owner, err)
	}
	if owner, exists := seen[relative]; exists && owner != ref.owner {
		return rooms.RoomReplayArtifact{}, mismatch("artifact ownership", fmt.Errorf("%q is claimed by %s and %s", relative, owner, ref.owner))
	}
	seen[relative] = ref.owner
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() {
		return rooms.RoomReplayArtifact{}, incomplete(ref.owner, fmt.Errorf("artifact %q is unavailable", relative))
	}
	if err := validateArtifactSize(artifact, info.Size(), ref.owner); err != nil {
		return rooms.RoomReplayArtifact{}, err
	}
	if err := validateArtifactDigest(artifact, absolute, ref.owner); err != nil {
		return rooms.RoomReplayArtifact{}, err
	}
	artifact.Owner, artifact.Role, artifact.AbsolutePath = ref.owner, ref.role, absolute
	return artifact, nil
}

func validateArtifactSize(artifact rooms.RoomReplayArtifact, size int64, owner string) error {
	if artifact.Size < 0 || size != artifact.Size {
		return mismatch(owner, fmt.Errorf("size does not match manifest"))
	}
	if artifact.Empty && (artifact.Size != 0 || size != 0) {
		return mismatch(owner, fmt.Errorf("empty artifact must have zero size"))
	}
	if !artifact.Empty && artifact.Size == 0 {
		return mismatch(owner, fmt.Errorf("zero size artifact must be marked empty"))
	}
	return nil
}

func validateArtifactDigest(artifact rooms.RoomReplayArtifact, path, owner string) error {
	digest, err := fileSHA256(path)
	if err != nil {
		return incomplete(owner, err)
	}
	if len(artifact.SHA256) != sha256.Size*2 || !strings.EqualFold(digest, artifact.SHA256) {
		return mismatch(owner, fmt.Errorf("sha256 does not match manifest"))
	}
	return nil
}

func safePath(root, raw string) (string, string, error) {
	clean := filepath.Clean(strings.TrimSpace(raw))
	if clean == "." || clean == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact path must be relative to the bundle")
	}
	absolute, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return "", "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if absolute != base && !strings.HasPrefix(absolute, base+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact path escapes the bundle")
	}
	return filepath.ToSlash(clean), absolute, nil
}

func fileSHA256(path string) (digest string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close %s after hashing: %w", path, closeErr))
		}
	}()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
