package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func (r *sandboxFs) executeRead(path string, fn func(root *os.Root, relPath string) error) error {
	rootPath, relPath, err := r.resolveRead(path)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer closeSandboxRoot(root)

	return fn(root, relPath)
}

func (r *sandboxFs) resolve(path string) (string, string, error) {
	roots, err := r.rootPaths()
	if err != nil {
		workdir := ""
		if r != nil {
			workdir = r.workspace
		}
		return "", "", newFilesystemAccessDeniedWithContext(workdir, FilesystemRefusalInvalidScope, err.Error())
	}

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)
	comparisonCandidate, err := canonicalizeExistingPath(candidate)
	if err != nil {
		// A path that cannot be canonicalized is not safe to authorize. In
		// particular, do not fall back to a lexical check when an existing
		// symlink or ancestor cannot be resolved: the root operation would be
		// making the authorization decision after the check.
		return "", "", newFilesystemAccessDeniedWithContext(r.filesystemWorkDir(), FilesystemRefusalOutsidePermittedRoots, "unable to resolve requested path")
	}
	if rootPath, relPath, ok, err := resolveCanonicalRoot(roots, comparisonCandidate); err != nil {
		return "", "", err
	} else if ok {
		return rootPath, relPath, nil
	}
	// A legacy restricted tool may be given a root that has already been
	// removed. Preserve its historical open-root diagnostic rather than
	// misclassifying the missing root as an escaping symlink. Validated
	// FilesystemPolicy roots always exist, so this fallback cannot widen a
	// policy-backed customer surface.
	if rootPath, relPath, ok, err := resolveMissingRoot(roots, candidate); err != nil {
		return "", "", err
	} else if ok {
		return rootPath, relPath, nil
	}
	if rootPath, relPath, ok, err := resolveLexicalRoot(roots, candidate); err != nil {
		return "", "", err
	} else if ok {
		if !r.enforceCanonical {
			// Legacy restricted constructors predate FilesystemPolicy and
			// retain their os.Root-based symlink enforcement and diagnostics.
			return rootPath, relPath, nil
		}
		// The lexical path is beneath a permitted root, but its canonical
		// target is not. This is the symlink-specific refusal and is
		// wrapped so callers can distinguish it without relying on text.
		return "", "", newFilesystemAccessDeniedWithContext(r.filesystemWorkDir(), FilesystemRefusalOutsidePermittedRoots, fmt.Sprintf("path escapes workspace: %s: %s", path, ErrFilesystemAccessDenied))
	}
	// Preserve the long-standing diagnostic for an ordinary absolute or
	// traversal path that was never lexically beneath a configured root.
	return "", "", newFilesystemAccessDeniedWithContext(r.filesystemWorkDir(), FilesystemRefusalOutsidePermittedRoots, fmt.Sprintf("path escapes workspace: %s", path))
}

func (r *sandboxFs) resolveRead(path string) (string, string, error) {
	if r.isProtectedRead(path) {
		denial := newFilesystemAccessDeniedWithContext(r.filesystemWorkDir(), FilesystemRefusalSensitiveRead, ErrFilesystemAccessDenied.Error())
		return "", "", fmt.Errorf("%w: %w", denial, ErrProtectedFilesystemRead)
	}
	return r.resolve(path)
}

func (r *sandboxFs) authorizeRead(path string) error {
	_, _, err := r.resolveRead(path)
	return err
}

func (r *sandboxFs) isProtectedRead(path string) bool {
	roots, err := r.rootPaths()
	if err != nil || len(roots) == 0 {
		return false
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(roots[0], candidate)
	}
	candidate = filepath.Clean(candidate)
	comparisonCandidate := candidate
	if resolved, err := canonicalizeExistingPath(candidate); err == nil {
		comparisonCandidate = resolved
	}
	protectedRoots := r.protectedReadRoots
	if len(protectedRoots) == 0 {
		protectedRoots = normalizeProtectedReadRoots(platformProtectedReadRoots())
	}
	for _, protectedRoot := range protectedRoots {
		if isWithinWorkspace(candidate, protectedRoot) || isWithinWorkspace(comparisonCandidate, protectedRoot) {
			return true
		}
	}
	return false
}

// canonicalizeExistingPath resolves the existing portion of a path and then
// appends its missing descendants. This keeps lexical containment comparisons
// correct when the platform exposes the same directory through a symlink
// alias (for example /var versus /private/var on macOS).
func canonicalizeExistingPath(path string) (string, error) {
	// NUL is not a valid filesystem path component. Leave it for the actual
	// os.Root operation to report so legacy tools retain their precise invalid
	// path diagnostic; no valid symlink can be hidden behind a NUL component.
	if strings.ContainsRune(path, '\x00') {
		return filepath.Clean(path), nil
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return canonicalizePathWithMissing(absolute, make(map[string]struct{}))
}

// canonicalizePathWithMissing resolves every existing path component,
// including dangling symlinks, and then appends any missing descendants. A
// plain EvalSymlinks call cannot resolve a dangling link, which would
// otherwise make a link to a future outside target look like an ordinary new
// in-root file.
func canonicalizePathWithMissing(path string, seen map[string]struct{}) (string, error) {
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := canonicalizeExistingNode(current, info, seen)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		// A missing descendant below an existing file reports ENOTDIR from
		// Lstat rather than ENOENT. Treat that as a missing path component so
		// the actual os.Root operation can return the accurate file/directory
		// shape diagnostic instead of misclassifying it as a scope escape.
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

func canonicalizeExistingNode(path string, info os.FileInfo, seen map[string]struct{}) (string, error) {
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
		return canonicalizePathWithMissing(target, seen)
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func (r *sandboxFs) rootPaths() ([]string, error) {
	if r == nil || strings.TrimSpace(r.workspace) == "" {
		return nil, fmt.Errorf("%s", workspaceUndefinedMessage)
	}
	rawRoots := make([]string, 0, 1+len(r.additionalWorkspaces))
	rawRoots = append(rawRoots, r.workspace)
	rawRoots = append(rawRoots, r.additionalWorkspaces...)
	roots := make([]string, 0, len(rawRoots))
	for _, rawRoot := range rawRoots {
		rootPath, err := filepath.Abs(rawRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve workspace path: %w", err)
		}
		roots = append(roots, filepath.Clean(rootPath))
	}
	return roots, nil
}

func isSandboxAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) || os.IsPermission(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "escapes from parent") ||
		strings.Contains(message, "outside root") ||
		strings.Contains(message, "outside of root") ||
		strings.Contains(message, "cross-device link")
}

func (r *sandboxFs) ReadFile(path string) ([]byte, error) {
	var content []byte
	err := r.executeRead(path, func(root *os.Root, relPath string) error {
		fileContent, err := root.ReadFile(relPath)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("failed to read file: file not found: %w", err)
			}
			if isSandboxAccessDenied(err) {
				return fmt.Errorf("failed to read file: access denied: %w", err)
			}
			return fmt.Errorf("failed to read file: %w", err)
		}
		content = fileContent
		return nil
	})
	return content, err
}

func (r *sandboxFs) WriteFile(path string, data []byte) error {
	return r.execute(path, func(root *os.Root, relPath string) error {
		return writeFileWithinRoot(root, relPath, data)
	})
}

func writeFileWithinRoot(root *os.Root, relPath string, data []byte) error {
	handled, err := writeExistingSymlink(root, relPath, data)
	if err != nil || handled {
		return err
	}
	dir := filepath.Dir(relPath)
	if err := makeSandboxParent(root, dir); err != nil {
		return err
	}
	if err := validateSandboxWriteTarget(root, relPath); err != nil {
		return err
	}
	return writeSandboxAtomically(root, relPath, dir, data)
}

func writeExistingSymlink(root *os.Root, relPath string, data []byte) (bool, error) {
	// Stat the target before creating the temporary file. Besides keeping
	// authorization ahead of side effects, this makes an existing symlink
	// to an external target fail closed instead of being replaced by an
	// otherwise-safe rename.
	_, err := root.Stat(relPath)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if isSandboxAccessDenied(err) {
			return false, fmt.Errorf("failed to authorize file: access denied: %w", err)
		}
		return false, nil
	}
	lstat, err := root.Lstat(relPath)
	if err != nil {
		return false, fmt.Errorf("failed to authorize existing target: %w", err)
	}
	if lstat.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	if err := root.WriteFile(relPath, data, sandboxFileMode); err != nil {
		if isSandboxAccessDenied(err) {
			return true, fmt.Errorf("failed to write file: access denied: %w", err)
		}
		return true, fmt.Errorf("failed to write file: %w", err)
	}
	return true, nil
}

func makeSandboxParent(root *os.Root, dir string) error {
	if dir == "." || dir == "/" {
		return nil
	}
	if err := root.MkdirAll(dir, sandboxDirectoryMode); err != nil {
		if isSandboxAccessDenied(err) {
			return fmt.Errorf("failed to create parent directories: access denied: %w", err)
		}
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	return nil
}

func writeSandboxAtomically(root *os.Root, relPath, dir string, data []byte) error {
	// Keep the staging file short and in the destination directory. The
	// root-owned operations preserve workspace confinement while retaining
	// write-then-rename atomicity.
	tmpFile, tmpRelPath, err := createSandboxWriteTempFile(root, dir)
	if err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	defer removeSandboxFileIfPresent(root, tmpRelPath)
	if err := writeAndCloseTempFile(tmpFile, data); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := root.Rename(tmpRelPath, relPath); err != nil {
		removeSandboxFileIfPresent(root, tmpRelPath)
		if isSandboxAccessDenied(err) {
			return fmt.Errorf("failed to rename temp file over target: access denied: %w", err)
		}
		return fmt.Errorf("failed to rename temp file over target: %w", err)
	}
	return nil
}

func removeSandboxFileIfPresent(root *os.Root, path string) {
	if err := root.Remove(path); err != nil && !os.IsNotExist(err) {
		return
	}
}

func (r *sandboxFs) ReadDir(path string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := r.executeRead(path, func(root *os.Root, relPath string) error {
		dirEntries, err := fs.ReadDir(root.FS(), filepath.ToSlash(relPath))
		if err != nil {
			return err
		}
		entries = dirEntries
		return nil
	})
	return entries, err
}

// Helper to get a safe relative path for os.Root usage
func getSafeRelPath(workspace, path string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("%s", workspaceUndefinedMessage)
	}

	rel := filepath.Clean(path)
	if filepath.IsAbs(rel) {
		var err error
		rel, err = filepath.Rel(workspace, rel)
		if err != nil {
			return "", fmt.Errorf("failed to calculate relative path: %w", err)
		}
	}

	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}

	return rel, nil
}
