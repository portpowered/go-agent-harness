package testing

import (
	"path/filepath"
	"runtime"
)

// SharedSessionFixturePath resolves a shared committed session fixture owned by
// go-llm-gateway/pkg/testing.
func SharedSessionFixturePath(name string) string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("testdata", "session-fixtures", name)
	}
	return filepath.Join(filepath.Dir(currentFile), "testdata", "session-fixtures", name)
}
