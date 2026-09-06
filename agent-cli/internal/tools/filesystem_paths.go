package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// authorizeFilesystemRead checks the canonical target of a read against the
// immutable host policy. It resolves existing symlinks and retains missing
// descendants so a dangling link cannot turn a future outside target into an
// apparently in-root path.
func authorizeFilesystemRead(policy *FilesystemPolicy, path string) error {
	if policy == nil {
		return nil
	}
	roots := policy.WritableRoots()
	if len(roots) == 0 {
		return newFilesystemAccessDeniedWithContext("", FilesystemRefusalInvalidScope, "workspace is not defined")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)
	canonicalCandidate, err := canonicalizeFilesystemPath(candidate)
	if err != nil {
		return newFilesystemAccessDeniedWithContext(policy.PrimaryRoot(), FilesystemRefusalOutsidePermittedRoots, "unable to resolve requested path")
	}
	for _, protectedRoot := range policy.ProtectedReadRoots() {
		if isWithinFilesystemPath(candidate, protectedRoot) || isWithinFilesystemPath(canonicalCandidate, protectedRoot) {
			denial := newFilesystemAccessDeniedWithContext(policy.PrimaryRoot(), FilesystemRefusalSensitiveRead, ErrFilesystemAccessDenied.Error())
			return fmt.Errorf("%w: %w", denial, ErrProtectedFilesystemRead)
		}
	}
	for _, root := range roots {
		canonicalRoot, rootErr := canonicalizeFilesystemPath(root)
		if rootErr != nil {
			return newFilesystemAccessDeniedWithContext(policy.PrimaryRoot(), FilesystemRefusalInvalidScope, rootErr.Error())
		}
		if isWithinFilesystemPath(canonicalCandidate, canonicalRoot) {
			return nil
		}
	}
	return newFilesystemAccessDeniedWithContext(policy.PrimaryRoot(), FilesystemRefusalOutsidePermittedRoots, fmt.Sprintf("path escapes workspace: %s", path))
}

func isWithinFilesystemPath(candidate, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && filepath.IsLocal(rel)
}

func canonicalizeFilesystemPath(path string) (string, error) {
	if strings.ContainsRune(path, '\x00') {
		return filepath.Clean(path), nil
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return canonicalizeFilesystemPathWithMissing(absolute, make(map[string]struct{}))
}

func canonicalizeFilesystemPathWithMissing(path string, seen map[string]struct{}) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := canonicalizeFilesystemNode(current, info, seen)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func canonicalizeFilesystemNode(path string, info os.FileInfo, seen map[string]struct{}) (string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			return "", fmt.Errorf("resolve symlink %q: too many levels of symbolic links", path)
		}
		seen[path] = struct{}{}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return canonicalizeFilesystemPathWithMissing(target, seen)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}
