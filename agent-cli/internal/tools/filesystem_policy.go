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

// ErrFilesystemAccessDenied identifies a filesystem operation rejected by the
// policy before it can expose or change host filesystem state.
var ErrFilesystemAccessDenied = errors.New("filesystem access denied")

// ErrProtectedFilesystemRead identifies a read denied because the resolved
// path belongs to a platform-protected system or credential location.
var ErrProtectedFilesystemRead = errors.New("protected filesystem read")

type filesystemAccessDeniedError struct {
	message string
	workdir string
	reason  FilesystemRefusalReason
}

func (e *filesystemAccessDeniedError) Error() string {
	return e.message
}

func (e *filesystemAccessDeniedError) Unwrap() error {
	return ErrFilesystemAccessDenied
}

func newFilesystemAccessDeniedWithContext(workdir string, reason FilesystemRefusalReason, message string) error {
	if reason == "" {
		reason = FilesystemRefusalOutsidePermittedRoots
	}
	return &filesystemAccessDeniedError{message: message, workdir: workdir, reason: reason}
}

// FilesystemPolicy is the immutable set of roots available to filesystem
// tools. The primary root is used to resolve relative tool paths; additional
// roots authorize absolute paths beneath those roots.
//
// Roots are converted to absolute paths and validated when the policy is
// constructed. The returned policy does not expose its backing slices, so
// callers cannot widen a live tool surface after construction.
type FilesystemPolicy struct {
	primaryRoot        string
	additionalRoots    []string
	protectedReadRoots []string
}

// FilesystemScopeStartupNotice is the stable customer-facing explanation
// printed alongside a resolved session scope. Shell-command deny patterns are
// intentionally described separately so disabling them cannot be mistaken for
// disabling filesystem confinement or for an operating-system sandbox.
const FilesystemScopeStartupNotice = "Filesystem tools are confined to the effective workdir and additional allowed roots; protected system and credential reads remain denied even when --allow-path includes them. Shell-command deny-pattern policy is separate, and this is not an operating-system sandbox."

// ResolveFilesystemPolicy captures and validates one immutable filesystem
// scope for a run. An empty primary root means the process current working
// directory. Relative additional roots are resolved against that captured
// primary root, not against a later process cwd.
func ResolveFilesystemPolicy(workdir string, additionalRoots ...string) (*FilesystemPolicy, error) {
	if strings.TrimSpace(workdir) == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("%w: get process working directory: %v", ErrInvalidFilesystemRoot, err)
		}
	}
	primary, err := validateFilesystemRoot("workdir", workdir)
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
		primaryRoot:        primary,
		additionalRoots:    roots,
		protectedReadRoots: normalizeProtectedReadRoots(platformProtectedReadRoots()),
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

// ProtectedReadRoots returns a copy of the platform-aware system and
// credential roots that remain unreadable even when a caller allowlists their
// containing directory for ordinary filesystem access.
func (p *FilesystemPolicy) ProtectedReadRoots() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.protectedReadRoots...)
}

// AuthorizeRead validates a path against this policy without opening or
// reading it. Filesystem tools use the same check immediately before their
// os.Root operation; this method is also the guard used by read_image before
// invoking a session-owned image preparer.
func (p *FilesystemPolicy) AuthorizeRead(path string) error {
	if p == nil {
		return nil
	}
	return authorizeFilesystemRead(p, path)
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

func normalizeProtectedReadRoots(rawRoots []string) []string {
	seen := make(map[string]struct{}, len(rawRoots)*2)
	roots := make([]string, 0, len(rawRoots)*2)
	add := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return
		}
		cleanPath := filepath.Clean(absPath)
		if _, exists := seen[cleanPath]; !exists {
			seen[cleanPath] = struct{}{}
			roots = append(roots, cleanPath)
		}
	}
	for _, rawRoot := range rawRoots {
		add(rawRoot)
		if resolved, err := filepath.EvalSymlinks(rawRoot); err == nil {
			// Keep both spellings. This matters on platforms such as macOS,
			// where /etc and /private/etc can name the same protected tree.
			add(resolved)
		}
	}
	return roots
}
