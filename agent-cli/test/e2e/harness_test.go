//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const scenarioTimeout = 8 * time.Minute

// runScenario keeps the manual entry points in this package while allowing
// scenario harnesses to live beside the internal implementation details they
// exercise. e2e_internal is deliberately private to this dispatcher.
func runScenario(t *testing.T, packagePath, testName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "test", "-tags=live,e2e_internal", "-count=1", packagePath, "-run", "^"+testName+"$", "-v")
	cmd.Dir = repositoryRoot(t)
	cmd.Env = os.Environ()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("%s timed out after %s\n%s", testName, scenarioTimeout, output.String())
		}
		t.Fatalf("%s failed: %v\n%s", testName, err, output.String())
	}
	t.Log(output.String())
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate E2E harness source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}
