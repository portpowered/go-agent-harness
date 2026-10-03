package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func (r *sandboxFs) executeRead(path string, fn func(guard *openGuard, relPath string) error) error {
	return r.withGuard(path, false, fn)
}

// withGuard runs one operation: the path pre-check, then fn with an
// open-time guard beneath the scope root. The pre-check refuses reads,
// writes, appends, edits and creates of protected system and credential
// locations before resolving the scope, so a broad root cannot expose
// ~/.ssh/id_rsa or plant ~/.ssh/authorized_keys. Both share one snapshot of the
// protected identities. afterPolicyCheck is the test seam between the check
// and the open.
func (r *sandboxFs) withGuard(path string, write bool, fn func(guard *openGuard, relPath string) error) error {
	protected := snapshotProtectedIdentities(r.protectedRoots())
	denied := r.protectedReadDenial
	if write {
		denied = r.protectedWriteDenial
	}
	if r.isProtectedIn(path, protected) {
		return denied()
	}
	rootPath, relPath, err := r.resolve(path)
	if err != nil {
		return err
	}
	if r.afterPolicyCheck != nil {
		r.afterPolicyCheck(path)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer closeSandboxRoot(root)
	return fn(newOpenGuard(root, protected, r.protectedRoots(), denied), relPath)
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

func (r *sandboxFs) protectedReadDenial() error {
	denial := newFilesystemAccessDeniedWithContext(r.filesystemWorkDir(), FilesystemRefusalSensitiveRead, ErrFilesystemAccessDenied.Error())
	return fmt.Errorf("%w: %w", denial, ErrProtectedFilesystemRead)
}

func (r *sandboxFs) protectedWriteDenial() error {
	denial := newFilesystemAccessDeniedWithContext(r.filesystemWorkDir(), FilesystemRefusalSensitiveWrite, ErrFilesystemAccessDenied.Error())
	return fmt.Errorf("%w: %w", denial, ErrProtectedFilesystemWrite)
}

// protectedRoots is the policy's protected roots, or the platform defaults
// for a legacy restricted tool built without them.
func (r *sandboxFs) protectedRoots() []string {
	if len(r.protectedReadRoots) == 0 {
		return normalizeProtectedReadRoots(platformProtectedReadRoots())
	}
	return r.protectedReadRoots
}

// authorizeRead is the read pre-check alone: it refuses a protected path
// before resolving the scope.
func (r *sandboxFs) authorizeRead(path string) error {
	if r.isProtected(path) {
		return r.protectedReadDenial()
	}
	_, _, err := r.resolve(path)
	return err
}

// isProtected reports whether path, lexically or after resolving symlinks,
// lies within a protected root. On case-insensitive platforms the comparison
// folds case per component, and any filesystem that names a protected root
// through another spelling is caught by comparing file identity.
func (r *sandboxFs) isProtected(path string) bool {
	return r.isProtectedIn(path, snapshotProtectedIdentities(r.protectedRoots()))
}

func (r *sandboxFs) isProtectedIn(path string, protected *protectedIdentities) bool {
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
	protectedRoots := r.protectedRoots()
	for _, protectedRoot := range protectedRoots {
		if isWithinProtectedRoot(candidate, protectedRoot) || isWithinProtectedRoot(comparisonCandidate, protectedRoot) {
			return true
		}
	}
	return protected.containsAncestorOf(comparisonCandidate)
}

// caseInsensitivePaths reports whether the platform's default filesystems
// compare names without regard to case (APFS/HFS+ on macOS, NTFS on
// Windows).
func caseInsensitivePaths() bool {
	return runtime.GOOS == darwinPlatform || runtime.GOOS == windowsPlatform
}

// isWithinProtectedRoot is isWithinWorkspace with per-component case folding
// on case-insensitive platforms, so ~/.SSH matches the ~/.ssh root.
func isWithinProtectedRoot(candidate, protectedRoot string) bool {
	if isWithinWorkspace(candidate, protectedRoot) {
		return true
	}
	if !caseInsensitivePaths() {
		return false
	}
	candidateParts := splitPathComponents(candidate)
	rootParts := splitPathComponents(protectedRoot)
	if len(candidateParts) < len(rootParts) {
		return false
	}
	for index, part := range rootParts {
		if !strings.EqualFold(part, candidateParts[index]) {
			return false
		}
	}
	return true
}

func splitPathComponents(path string) []string {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	rest := strings.Trim(clean[len(volume):], string(filepath.Separator))
	parts := []string{volume}
	if rest != "" {
		parts = append(parts, strings.Split(rest, string(filepath.Separator))...)
	}
	return parts
}

// hasProtectedAncestor reports whether an existing ancestor of candidate (or
// candidate itself) is the same file as an existing protected root. This
// catches every spelling a case-insensitive or case-folding filesystem
// accepts, on any platform.
func hasProtectedAncestor(candidate string, protectedRoots []string) bool {
	return snapshotProtectedIdentities(protectedRoots).containsAncestorOf(candidate)
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
	err := r.executeRead(path, func(guard *openGuard, relPath string) error {
		fileContent, err := guard.readFile(relPath)
		if err != nil {
			if errors.Is(err, ErrFilesystemAccessDenied) {
				return err
			}
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
	return r.execute(path, func(guard *openGuard, relPath string) error {
		return guard.writeFile(relPath, data)
	})
}

func (r *sandboxFs) ReadDir(path string) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := r.executeRead(path, func(guard *openGuard, relPath string) error {
		dirEntries, err := guard.readDir(relPath)
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
