package flags

import (
	"os"
	"testing"
)

// TestNewGlobalFlagsDefaultWorkDirUsesLaunchDirectory proves the process host
// boundary reports the launch directory as the default working directory.
func TestNewGlobalFlagsDefaultWorkDirUsesLaunchDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	launchDir, err := os.Getwd() //nolint:forbidigo // the test observes the process launch directory the host boundary must report
	if err != nil {
		t.Fatalf("get launch directory: %v", err)
	}

	workDir, err := NewGlobalFlags().EffectiveWorkDir()
	if err != nil {
		t.Fatalf("EffectiveWorkDir: %v", err)
	}
	if workDir != launchDir {
		t.Fatalf("default work directory = %q, want launch directory %q", workDir, launchDir)
	}
}
