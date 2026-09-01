//go:build windows

package tools

import (
	"os"
	"path/filepath"
)

// platformProtectedReadRoots lists Windows operating-system data and common
// user credential stores. Environment-derived roots cover relocated Windows
// and ProgramData installations instead of assuming a particular drive.
func platformProtectedReadRoots() []string {
	roots := []string{}
	for _, key := range []string{"WINDIR", "SYSTEMROOT", "PROGRAMDATA"} {
		if value := os.Getenv(key); value != "" {
			roots = append(roots, value)
		}
	}
	if systemDrive := os.Getenv("SystemDrive"); systemDrive != "" {
		roots = append(roots, filepath.Join(systemDrive, "Windows"), filepath.Join(systemDrive, "ProgramData"))
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
		filepath.Join(home, ".kube"),
		filepath.Join(home, "AppData", "Local", "Microsoft", "Credentials"),
		filepath.Join(home, "AppData", "Local", "Microsoft", "IdentityCache"),
		filepath.Join(home, "AppData", "Roaming", "Microsoft", "Credentials"),
		filepath.Join(home, "AppData", "Roaming", "gcloud"),
	)
	return roots
}
