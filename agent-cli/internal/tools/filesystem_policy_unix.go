//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"runtime"
)

// platformProtectedReadRoots lists the Unix system trees and common per-user
// credential stores that filesystem tools must never read through a widened
// policy. The list is intentionally path-based and independent of file
// permissions: a readable secret is still a protected secret.
func platformProtectedReadRoots() []string {
	roots := []string{
		"/etc",
		"/private/etc",
		"/proc",
		"/sys",
		"/dev",
		"/boot",
		"/root",
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return roots
	}
	roots = append(roots,
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".azure"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".config", "gcloud"),
		filepath.Join(home, ".config", "gh"),
		filepath.Join(home, ".config", "hub"),
		filepath.Join(home, ".local", "share", "keyrings"),
		filepath.Join(home, ".git-credentials"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".pypirc"),
	)
	if runtime.GOOS == "darwin" {
		roots = append(roots,
			filepath.Join(home, "Library", "Keychains"),
			"/var/root/Library/Keychains",
		)
	}
	return roots
}
