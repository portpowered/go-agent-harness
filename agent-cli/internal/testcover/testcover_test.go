package testcover

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fixtureEnv makes this test binary act as an exiting fixture process.
const fixtureEnv = "AGENT_CLI_TESTCOVER_FIXTURE"

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnv) != "" {
		MustIsolateFixtureProcess()
		os.Exit(0) // coverage exit hooks write into the isolated directory
	}
	os.Exit(m.Run())
}

func TestIsolateFixtureProcessUsesAPrivateSubdirectory(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("GOCOVERDIR", parent)
	if err := IsolateFixtureProcess(); err != nil {
		t.Fatalf("IsolateFixtureProcess: %v", err)
	}
	dir := os.Getenv("GOCOVERDIR")
	if filepath.Dir(dir) != parent || !strings.HasPrefix(filepath.Base(dir), "fixture-") {
		t.Fatalf("GOCOVERDIR = %q, want a fixture- directory inside %q", dir, parent)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("isolated coverage directory = %v, %v", info, err)
	}
}

func TestMustIsolateFixtureProcessIsolates(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("GOCOVERDIR", parent)
	MustIsolateFixtureProcess()
	if dir := os.Getenv("GOCOVERDIR"); filepath.Dir(dir) != parent {
		t.Fatalf("GOCOVERDIR = %q, want a directory inside %q", dir, parent)
	}
}

func TestMustIsolateFixtureProcessExitsWithStatusTwoOnFailure(t *testing.T) {
	t.Setenv("GOCOVERDIR", filepath.Join(t.TempDir(), "missing"))
	var reported error
	status := -1
	mustIsolateFixtureProcess(func(err error) { reported = err }, func(code int) { status = code })
	if status != 2 || reported == nil || !strings.Contains(reported.Error(), "isolate fixture coverage directory") {
		t.Fatalf("exit status %d, reported %v; want status 2 and the isolation error", status, reported)
	}
}

func TestIsolateFixtureProcessWithoutCoverageDoesNothing(t *testing.T) {
	t.Setenv("GOCOVERDIR", "")
	if err := IsolateFixtureProcess(); err != nil || os.Getenv("GOCOVERDIR") != "" {
		t.Fatalf("IsolateFixtureProcess without GOCOVERDIR = %v, GOCOVERDIR %q", err, os.Getenv("GOCOVERDIR"))
	}
}

func TestIsolateFixtureProcessReportsAnUnusableCoverageDirectory(t *testing.T) {
	t.Setenv("GOCOVERDIR", filepath.Join(t.TempDir(), "missing"))
	if err := IsolateFixtureProcess(); err == nil {
		t.Fatal("IsolateFixtureProcess accepted a missing GOCOVERDIR")
	}
}

// Without isolation, a few of 64 fixture children exiting together in a
// coverage run print "coverage meta-data emit failed ... no such file or
// directory". With it, every child exits silently.
func TestConcurrentFixtureProcessesExitWithoutCoverageErrors(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu       sync.Mutex
		failures []string
		wg       sync.WaitGroup
	)
	for range 64 {
		wg.Go(func() {
			cmd := exec.CommandContext(t.Context(), executable)
			cmd.Env = append(os.Environ(), fixtureEnv+"=1")
			output, err := cmd.CombinedOutput()
			if err != nil || len(output) != 0 {
				mu.Lock()
				failures = append(failures, fmt.Sprintf("%v: %q", err, output))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(failures) != 0 {
		t.Fatalf("%d fixture processes failed, first: %s", len(failures), failures[0])
	}
}
