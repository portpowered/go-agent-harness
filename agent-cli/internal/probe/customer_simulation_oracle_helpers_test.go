package probe

import (
	"fmt"
	"os"
	"path/filepath"
)

// FilesystemDirectorySHA256 returns the deterministic fingerprint used for
// directory checkpoint entries. It includes the relative type of every
// descendant and the content hash of regular files, without following
// symlinks. A directory checkpoint therefore proves more than just a path
// type while remaining stable across machines.
func FilesystemDirectorySHA256(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve directory: %w", ErrFilesystemOracleInvalidRoot, err)
	}
	info, err := os.Lstat(absRoot)
	if err != nil {
		return "", fmt.Errorf("%w: inspect directory: %w", ErrFilesystemOracleInvalidRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: %q is not a non-symlink directory", ErrFilesystemOracleInvalidRoot, absRoot)
	}
	return filesystemDirectorySHA256(absRoot)
}
