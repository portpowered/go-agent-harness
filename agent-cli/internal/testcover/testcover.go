// Package testcover keeps coverage output of test-binary fixture processes
// from colliding.
package testcover

import (
	"fmt"
	"os"
)

// IsolateFixtureProcess gives a test-binary child that exits from a TestMain
// fixture mode, without running tests, its own coverage directory. Call it
// first in that branch.
//
// Such a child writes covmeta.<hash> into GOCOVERDIR from an exit hook
// through a temporary file named only by the exit time, and it always
// rewrites the file (the runtime's size check never matches an existing
// one). On macOS that time has microsecond resolution, so two fixture
// children that exit in the same microsecond create the same temporary
// file: one renames it, and the other's rename fails with "no such file or
// directory" and prints a coverage error on its stderr.
//
// The directory is created inside the inherited GOCOVERDIR, so it is removed
// with it, and the test binary's coverage merge skips subdirectories. A
// fixture runs test code only, so its counters carry no coverage; a child
// that runs tests already writes to a private directory of its own. Outside
// a coverage run (GOCOVERDIR unset) it does nothing.
func IsolateFixtureProcess() error {
	parent := os.Getenv("GOCOVERDIR")
	if parent == "" {
		return nil
	}
	dir, err := os.MkdirTemp(parent, "fixture-")
	if err != nil {
		return fmt.Errorf("isolate fixture coverage directory: %w", err)
	}
	if err := os.Setenv("GOCOVERDIR", dir); err != nil {
		return fmt.Errorf("isolate fixture coverage directory: %w", err)
	}
	return nil
}

// MustIsolateFixtureProcess is IsolateFixtureProcess for a TestMain fixture
// branch: on failure it reports the error on stderr and exits with status 2.
func MustIsolateFixtureProcess() {
	if err := IsolateFixtureProcess(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
