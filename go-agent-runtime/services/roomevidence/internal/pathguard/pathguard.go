// Package pathguard provides the shared bundle path safety checks used by
// admission and audio projection. It does not resolve or retain user data.
package pathguard

import (
	"os"
	"path/filepath"
	"strings"
)

type validationError string

func (e validationError) Error() string { return string(e) }

const (
	errSymlink    validationError = "bundle path contains a symlink"
	errOutside    validationError = "bundle path is outside its root"
	errValidation validationError = "bundle path cannot be revalidated"
)

// ValidateNoSymlink rejects a path containing a symlinked component and then
// revalidates the resolved path remains under the resolved bundle root. Missing
// final components are left to the caller so it can preserve its missing-file
// error classification.
func ValidateNoSymlink(root, path string) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(path) == "" {
		return errValidation
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return errValidation
	}
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return errValidation
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || !withinRoot(relative) {
		return errOutside
	}
	if err := rejectSymlinkComponents(rootAbs, relative); err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errValidation
	}
	resolvedPath, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errValidation
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || !withinRoot(resolvedRelative) {
		return errOutside
	}
	return nil
}

// ValidateNoSymlinkPath rejects symlink components in an output path. Unlike
// ValidateNoSymlink, this helper has no existing bundle root to compare
// against; it walks every existing component from the filesystem root and
// leaves missing descendants for the caller to create.
func ValidateNoSymlinkPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errValidation
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return errValidation
	}
	volume := filepath.VolumeName(absolute)
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, root)
	if relative == absolute {
		return errValidation
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return errValidation
		}
		if info.Mode()&os.ModeSymlink != 0 && !allowedSystemSymlink(root, current) {
			return errSymlink
		}
	}
	return nil
}

func allowedSystemSymlink(root, path string) bool {
	if filepath.Dir(path) != filepath.Clean(root) {
		return false
	}
	switch filepath.Base(path) {
	case "tmp", "var":
		// macOS exposes these standard writable locations as root-level
		// aliases. The caller-controlled components below them are still
		// checked individually.
		return true
	default:
		return false
	}
}

func rejectSymlinkComponents(root, relative string) error {
	if err := rejectRoot(root); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if err := rejectComponent(current); err != nil {
			return err
		}
	}
	return nil
}

func rejectRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errValidation
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errSymlink
	}
	return nil
}

func rejectComponent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errValidation
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errSymlink
	}
	return nil
}

func withinRoot(relative string) bool {
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
