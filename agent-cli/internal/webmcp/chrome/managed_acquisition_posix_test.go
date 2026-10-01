//go:build !windows

package chrome

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestQueryChromeVersionRunsTheExecutable runs the production version query
// against a system binary, which needs no first-run code assessment.
func TestQueryChromeVersionRunsTheExecutable(t *testing.T) {
	const echo = "/bin/echo"
	output, err := queryChromeVersion(context.Background(), echo)
	if err != nil || strings.TrimSpace(output) == "" {
		t.Fatalf("queryChromeVersion(%s) = %q, %v; want the executable's output", echo, output, err)
	}
	if _, err := queryChromeVersion(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("queryChromeVersion succeeded for a missing executable")
	}
}
