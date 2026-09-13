//go:build !linux && !darwin && !windows

package service

import "fmt"

// The supported desktop targets use a native no-replace primitive. Keep other
// Go targets buildable without falling back to an overwriting rename.
func renameNoReplace(oldPath, newPath string) error {
	return fmt.Errorf("atomic no-replace publication is unavailable for %q -> %q", oldPath, newPath)
}
