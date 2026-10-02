package flags

import (
	"errors"
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

// TestHostDirLookupsReportUnavailableDirectories proves a missing, failing or
// empty host lookup is reported as an unavailable directory, never as "".
func TestHostDirLookupsReportUnavailableDirectories(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	for _, test := range []struct {
		name   string
		lookup func() (string, error)
		cause  error
	}{
		{name: "missing lookup"},
		{name: "failing lookup", lookup: func() (string, error) { return "", lookupErr }, cause: lookupErr},
		{name: "empty directory", lookup: func() (string, error) { return "", nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			flags := &GlobalFlags{Host: HostDirs{HomeDir: test.lookup, WorkDir: test.lookup}}
			if _, err := flags.HostHomeDir(); !errors.Is(err, errHomeDirUnavailable) || (test.cause != nil && !errors.Is(err, test.cause)) {
				t.Fatalf("HostHomeDir error = %v, want home directory unavailable", err)
			}
			if _, err := flags.HostWorkDir(); !errors.Is(err, errWorkDirUnavailable) || (test.cause != nil && !errors.Is(err, test.cause)) {
				t.Fatalf("HostWorkDir error = %v, want working directory unavailable", err)
			}
			if got := flags.ConfigDir(); got != "" {
				t.Fatalf("ConfigDir without a home directory = %q, want empty", got)
			}
			if got := flags.HostWorkDirOrEmpty(); got != "" {
				t.Fatalf("HostWorkDirOrEmpty = %q, want empty", got)
			}
		})
	}
	var nilFlags *GlobalFlags
	if nilFlags.ConfigDir() != "" || nilFlags.WorkDir() != "" || nilFlags.AllowPaths() != nil {
		t.Fatal("nil flags must report no config dir, workdir or allowed paths")
	}
	if _, err := nilFlags.HostHomeDir(); !errors.Is(err, errHomeDirUnavailable) {
		t.Fatalf("nil flags HostHomeDir error = %v", err)
	}
}
