//go:build live

package e2e

import (
	"path/filepath"
	"runtime"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate E2E harness source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
