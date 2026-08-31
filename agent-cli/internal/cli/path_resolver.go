package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// PathResolutionError identifies a user-supplied path that could not be
// resolved before command dispatch. Keeping the original value on the error
// makes failures actionable without requiring callers to reconstruct it.
type PathResolutionError struct {
	Path string
	Err  error
}

func (e *PathResolutionError) Error() string {
	if e == nil {
		return "path resolution failed"
	}
	return fmt.Sprintf("resolve path %q: %v", e.Path, e.Err)
}

func (e *PathResolutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// pathResolver expands only leading-tilde filesystem paths. The lookup
// functions are injected so command preflight can be tested without changing
// process-wide home state or touching the filesystem.
type pathResolver struct {
	currentHome func() (string, error)
	lookupUser  func(string) (string, error)
}

func newPathResolver() *pathResolver {
	return &pathResolver{
		currentHome: os.UserHomeDir,
		lookupUser: func(name string) (string, error) {
			account, err := user.Lookup(name)
			if err != nil {
				return "", err
			}
			return account.HomeDir, nil
		},
	}
}

// Resolve expands ~, ~/path, ~user, and ~user/path. Empty values and paths
// without a leading tilde are returned unchanged so callers retain their
// existing sentinel, relative-path, URL, and default-value semantics.
func (r *pathResolver) Resolve(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, "~") {
		return value, nil
	}
	if strings.ContainsRune(value, '\x00') {
		return "", &PathResolutionError{Path: value, Err: fmt.Errorf("malformed leading-tilde path: contains NUL byte")}
	}

	username, suffix, err := splitLeadingTildePath(value)
	if err != nil {
		return "", &PathResolutionError{Path: value, Err: err}
	}

	if r == nil {
		r = newPathResolver()
	}
	var home string
	if username == "" {
		if r.currentHome == nil {
			return "", &PathResolutionError{Path: value, Err: fmt.Errorf("current home lookup is unavailable")}
		}
		home, err = r.currentHome()
		if err != nil {
			return "", &PathResolutionError{Path: value, Err: fmt.Errorf("current home lookup failed: %w", err)}
		}
	} else {
		if r.lookupUser == nil {
			return "", &PathResolutionError{Path: value, Err: fmt.Errorf("named-user lookup is unavailable")}
		}
		home, err = r.lookupUser(username)
		if err != nil {
			return "", &PathResolutionError{Path: value, Err: fmt.Errorf("lookup home for user %q failed: %w", username, err)}
		}
	}
	if home == "" {
		who := "current user"
		if username != "" {
			who = fmt.Sprintf("user %q", username)
		}
		return "", &PathResolutionError{Path: value, Err: fmt.Errorf("home directory for %s is empty", who)}
	}

	home, err = filepath.Abs(home)
	if err != nil {
		return "", &PathResolutionError{Path: value, Err: fmt.Errorf("make home directory absolute: %w", err)}
	}
	resolved, err := filepath.Abs(filepath.Join(home, filepath.FromSlash(suffix)))
	if err != nil {
		return "", &PathResolutionError{Path: value, Err: fmt.Errorf("make expanded path absolute: %w", err)}
	}
	return resolved, nil
}

func splitLeadingTildePath(value string) (username, suffix string, err error) {
	rest := value[1:]
	if rest == "" {
		return "", "", nil
	}

	if isTildePathSeparator(rest[0]) {
		return "", strings.TrimLeft(rest, "/\\"), nil
	}

	separator := strings.IndexAny(rest, "/\\")
	if separator < 0 {
		return rest, "", nil
	}
	username = rest[:separator]
	if username == "" {
		return "", "", fmt.Errorf("malformed leading-tilde path: missing username")
	}
	return username, strings.TrimLeft(rest[separator:], "/\\"), nil
}

func isTildePathSeparator(value byte) bool {
	return value == '/' || value == '\\'
}
