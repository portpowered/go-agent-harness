package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidFilesystemRoot identifies a workdir or additional filesystem root
// that cannot be used as an authorization boundary.
var ErrInvalidFilesystemRoot = errors.New("invalid filesystem root")

// FilesystemPolicy is the immutable set of roots available to filesystem
// tools. The primary root is used to resolve relative tool paths; additional
// roots authorize absolute paths beneath those roots.
//
// Roots are converted to absolute paths and validated when the policy is
// constructed. The returned policy does not expose its backing slices, so
// callers cannot widen a live tool surface after construction.
type FilesystemPolicy struct {
	primaryRoot     string
	additionalRoots []string
}

// NewFilesystemPolicy validates the primary root and any additional roots.
// Relative root arguments are resolved against the process working directory
// at construction time. Callers that need startup-captured cwd semantics
// should resolve their flags before calling this constructor.
func NewFilesystemPolicy(primaryRoot string, additionalRoots ...string) (*FilesystemPolicy, error) {
	primary, err := validateFilesystemRoot("primary", primaryRoot)
	if err != nil {
		return nil, err
	}

	roots := make([]string, 0, len(additionalRoots))
	seen := map[string]struct{}{primary: {}}
	for index, candidate := range additionalRoots {
		root, err := validateFilesystemRoot(fmt.Sprintf("additional[%d]", index), candidate)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	return &FilesystemPolicy{
		primaryRoot:     primary,
		additionalRoots: roots,
	}, nil
}

// NewFilesystemPolicyFromRoots is the slice-taking form of
// NewFilesystemPolicy for callers that already collect repeatable roots.
func NewFilesystemPolicyFromRoots(primaryRoot string, additionalRoots []string) (*FilesystemPolicy, error) {
	return NewFilesystemPolicy(primaryRoot, additionalRoots...)
}

// PrimaryRoot returns the canonical primary filesystem root.
func (p *FilesystemPolicy) PrimaryRoot() string {
	if p == nil {
		return ""
	}
	return p.primaryRoot
}

// AdditionalRoots returns a copy of the canonical additional roots.
func (p *FilesystemPolicy) AdditionalRoots() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.additionalRoots...)
}

// WritableRoots returns the primary root followed by the additional roots.
// The copy prevents a caller from mutating a policy after it has been passed
// to a filesystem tool.
func (p *FilesystemPolicy) WritableRoots() []string {
	if p == nil || p.primaryRoot == "" {
		return nil
	}
	roots := make([]string, 0, 1+len(p.additionalRoots))
	roots = append(roots, p.primaryRoot)
	roots = append(roots, p.additionalRoots...)
	return roots
}

func validateFilesystemRoot(label, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: %s root is empty", ErrInvalidFilesystemRoot, label)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s root %q: %v", ErrInvalidFilesystemRoot, label, path, err)
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s root %q: %v", ErrInvalidFilesystemRoot, label, path, err)
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return "", fmt.Errorf("%w: normalize %s root %q: %v", ErrInvalidFilesystemRoot, label, path, err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("%w: inspect %s root %q: %v", ErrInvalidFilesystemRoot, label, path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s root %q is not a directory", ErrInvalidFilesystemRoot, label, path)
	}
	// Keep the absolute spelling supplied by the caller for path matching.
	// The filesystem root itself is opened by os.OpenRoot, which anchors all
	// subsequent operations to the directory validated above. Retaining this
	// spelling also keeps platform aliases such as /var and /private/var
	// consistent between the requested path and the configured root.
	return filepath.Clean(absPath), nil
}
