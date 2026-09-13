package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	roomReplayMaxManifestBytes     int64 = 4 << 20
	roomReplayMaxTimelineBytes     int64 = 32 << 20
	roomReplayMaxTimelineLineBytes int64 = 4 << 20
	roomReplayMaxTimelineRecords   int64 = 100000
	roomReplayMaxArtifactBytes     int64 = 256 << 20
	roomReplayMaxCaptureBytes      int64 = 16 << 20
)

func resolveRoomReplayBundle(bundle string) (string, string, string, error) {
	raw := strings.TrimSpace(bundle)
	if raw == "" {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleIncomplete, "bundle", "", "directory or manifest path", "missing", ErrRoomReplayBundleIncomplete)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "bundle", "", "resolvable path", raw, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		kind := RoomReplayBundleMismatch
		if errors.Is(err, os.ErrNotExist) {
			kind = RoomReplayBundleIncomplete
		}
		return "", "", "", newRoomReplayBundleError(kind, "bundle", filepath.ToSlash(abs), "existing bundle", err.Error(), err)
	}

	var rootCandidate, manifestRelative string
	if info.IsDir() {
		rootCandidate = abs
		manifestRelative = RoomReplayBundleManifestPath
	} else {
		rootCandidate = filepath.Dir(abs)
		manifestRelative = filepath.Base(abs)
	}
	root, err := filepath.EvalSymlinks(rootCandidate)
	if err != nil {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleIncomplete, "bundle", filepath.ToSlash(rootCandidate), "resolvable bundle directory", err.Error(), err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "bundle", rootCandidate, "absolute bundle directory", err.Error(), err)
	}
	manifestPath, normalized, pathErr := safeRoomReplayPath(root, manifestRelative)
	if pathErr != nil {
		return "", "", "", pathErr
	}
	return root, manifestPath, normalized, nil
}

func readRoomReplayManifest(path string) (data []byte, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			data = nil
			err = closeErr
		}
	}()
	data, err = io.ReadAll(io.LimitReader(file, roomReplayMaxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > roomReplayMaxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds maximum size of %d bytes", roomReplayMaxManifestBytes)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("manifest is empty")
	}
	return data, nil
}

func safeRoomReplayPath(root, relative string) (string, string, error) {
	value := strings.TrimSpace(relative)
	normalized, err := normalizeSafeRoomReplayPath(value)
	if err != nil {
		return "", "", err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "bundle root", err.Error(), err)
	}
	joined := filepath.Join(absoluteRoot, filepath.FromSlash(normalized))
	rel, err := filepath.Rel(absoluteRoot, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "path confined to bundle", rel, ErrInvalidRoomReplayBundle)
	}
	if err := rejectRoomReplaySymlinkComponents(absoluteRoot, normalized); err != nil {
		return "", "", err
	}
	return joined, normalized, nil
}

func normalizeSafeRoomReplayPath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\\') {
		return "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "bundle-relative slash path", "unsafe path", ErrInvalidRoomReplayBundle)
	}
	if filepath.IsAbs(value) || path.IsAbs(value) || filepath.VolumeName(value) != "" || strings.HasPrefix(value, "//") {
		return "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "bundle-relative path", "absolute path", ErrInvalidRoomReplayBundle)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "path without traversal", ".. component", ErrInvalidRoomReplayBundle)
		}
	}
	normalized := path.Clean(value)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, ":") {
		return "", newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", value, "non-empty bundle-relative path", normalized, ErrInvalidRoomReplayBundle)
	}
	return normalized, nil
}

func rejectRoomReplaySymlinkComponents(root, normalized string) error {
	current := root
	for _, component := range strings.Split(normalized, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", normalized, "inspectable path components", err.Error(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact.path", normalized, "non-symlink artifact path", "symlink component", ErrInvalidRoomReplayBundle)
		}
	}
	return nil
}

func roomReplayPathKey(value string) string {
	return path.Clean(filepath.ToSlash(strings.TrimSpace(value)))
}

func validateRoomReplayInventory(root string, inventory []roomReplayArtifactRef, byPath map[string]RoomReplayArtifact) error {
	for _, entry := range inventory {
		_, normalized, err := safeRoomReplayPath(root, entry.Path)
		if err != nil {
			return err
		}
		if _, ok := byPath[normalized]; !ok {
			return newRoomReplayBundleError(RoomReplayBundleMismatch, entry.Field, normalized, "referenced artifact ownership", "orphan integrity entry", ErrInvalidRoomReplayBundle)
		}
	}
	return nil
}

func roomReplayFileDigest(filename string) (digest string, err error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			digest = ""
			err = closeErr
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > roomReplayMaxArtifactBytes {
		return "", fmt.Errorf("artifact exceeds maximum size of %d bytes", roomReplayMaxArtifactBytes)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, roomReplayMaxArtifactBytes+1))
	if err != nil {
		return "", err
	}
	if written > roomReplayMaxArtifactBytes {
		return "", fmt.Errorf("artifact exceeds maximum size of %d bytes", roomReplayMaxArtifactBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
