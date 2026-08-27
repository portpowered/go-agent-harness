package testing

import (
	"os"
	"path/filepath"
	"runtime"
)

const sharedSessionFixtureRootRelPath = "go-llm-gateway/pkg/testing/testdata/session-fixtures"

// SharedSessionFixturePath resolves a shared committed session fixture owned by
// go-llm-gateway/pkg/testing.
func SharedSessionFixturePath(name string) string {
	_, currentFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(currentFile) {
		return filepath.Join(filepath.Dir(currentFile), "testdata", "session-fixtures", name)
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		if repositoryRoot, found := findRepositoryRoot(workingDirectory); found {
			return filepath.Join(repositoryRoot, filepath.FromSlash(sharedSessionFixtureRootRelPath), name)
		}
	}
	return filepath.Join("testdata", "session-fixtures", name)
}

func findRepositoryRoot(start string) (string, bool) {
	absoluteStart, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	directory := filepath.Clean(absoluteStart)
	for {
		workFile := filepath.Join(directory, "go.work")
		if info, err := os.Stat(workFile); err == nil && !info.IsDir() {
			return directory, true
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", false
		}
		directory = parent
	}
}
