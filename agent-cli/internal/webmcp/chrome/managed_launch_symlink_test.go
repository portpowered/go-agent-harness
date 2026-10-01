//go:build !windows

package chrome

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedBrowserLauncherRejectsSymlinkedProfile needs unprivileged
// symlink creation, which Windows does not grant by default.
func TestManagedBrowserLauncherRejectsSymlinkedProfile(t *testing.T) {
	configDir := t.TempDir()
	profileTarget := t.TempDir()
	profile := filepath.Join(configDir, ManagedBrowserProfileDirName)
	if err := os.Symlink(profileTarget, profile); err != nil {
		t.Fatalf("create symlinked profile: %v", err)
	}
	process := &managedLaunchTestProcess{}
	launcher := newManagedLaunchTestLauncher(t, process, nil, nil)
	launcher.options.ConfigDir = configDir
	_, err := launcher.Launch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "during profile") {
		t.Fatalf("symlinked profile error = %v, want profile phase", err)
	}
	if process.startCalls.Load() != 0 {
		t.Fatal("symlinked profile started Chrome")
	}
}
