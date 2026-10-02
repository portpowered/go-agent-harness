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

// FilesystemScopeStartupNotice is the stable customer-facing explanation
// printed alongside a resolved session scope. Shell-command deny patterns are
// intentionally described separately so disabling them cannot be mistaken for
// disabling filesystem confinement or for an operating-system sandbox.
const FilesystemScopeStartupNotice = "Filesystem tools are confined to the effective workdir and additional allowed roots; protected system and credential locations can be neither read nor written even when --allow-path includes them. Shell-command deny-pattern policy is separate, and this is not an operating-system sandbox."

// FilesystemHost carries the host directories a filesystem policy is
// resolved against. The CLI host boundary captures them once per run.
type FilesystemHost struct {
	// WorkDir is the effective primary root (--workdir, else the process
	// working directory captured at startup).
	WorkDir string
}

// ResolveFilesystemPolicy captures and validates one immutable filesystem
// scope for a run. Relative additional roots are resolved against the
// captured primary root, not against a later process cwd.
func ResolveFilesystemPolicy(host FilesystemHost, additionalRoots ...string) (*FilesystemPolicy, error) {
	if strings.TrimSpace(host.WorkDir) == "" {
		return nil, fmt.Errorf("%w: workdir is required", ErrInvalidFilesystemRoot)
	}
	primary, err := validateFilesystemRoot("workdir", host.WorkDir)
	if err != nil {
		return nil, err
	}
	resolvedAdditional := make([]string, 0, len(additionalRoots))
	for _, root := range additionalRoots {
		if !filepath.IsAbs(root) {
			root = filepath.Join(primary, root)
		}
		resolvedAdditional = append(resolvedAdditional, root)
	}
	return NewFilesystemPolicy(primary, resolvedAdditional...)
}

// ScopeDescription is the stable human-readable representation used by
// startup output and the agent operating context.
func (p *FilesystemPolicy) ScopeDescription() string {
	if p == nil {
		return "filesystem scope unavailable"
	}
	additional := "none"
	if len(p.additionalRoots) > 0 {
		additional = strings.Join(p.additionalRoots, ",")
	}
	return fmt.Sprintf("workdir=%s; additional_allowed_roots=%s", p.primaryRoot, additional)
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

func validateFilesystemRoot(label, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: %s root is empty", ErrInvalidFilesystemRoot, label)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s root %q: %w", ErrInvalidFilesystemRoot, label, path, err)
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %s root %q: %w", ErrInvalidFilesystemRoot, label, path, err)
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return "", fmt.Errorf("%w: normalize %s root %q: %w", ErrInvalidFilesystemRoot, label, path, err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("%w: inspect %s root %q: %w", ErrInvalidFilesystemRoot, label, path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s root %q is not a directory", ErrInvalidFilesystemRoot, label, path)
	}
	return filepath.Clean(realPath), nil
}
