package architecture

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPhase3SharedContractDependencyDirection(t *testing.T) {
	t.Run("gateway adapters depend on loop-owned contracts", func(t *testing.T) {
		imports := goListLines(t, repoRoot(t), "-f", "{{join .Imports \"\\n\"}}", "./go-llm-gateway/pkg/inference")
		if !containsLine(imports, "github.com/portpowered/go-agent-loop/pkg/messages") {
			t.Fatalf("go-llm-gateway/pkg/inference imports = %v, want loop-owned shared contract import", imports)
		}
	})

	t.Run("loop packages do not depend on gateway packages", func(t *testing.T) {
		deps := goListLines(t, repoRoot(t), "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./go-agent-loop/...")
		for _, dep := range deps {
			if strings.HasPrefix(dep, "github.com/portpowered/go-llm-gateway/") {
				t.Fatalf("go-agent-loop dependency graph contains reverse import %q; Phase 3 shared contract boundary requires go-agent-loop to stay below go-llm-gateway", dep)
			}
		}
	})
}

func goListLines(t *testing.T, dir string, args ...string) []string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdArgs := append([]string{"list"}, args...)
	cmd := exec.CommandContext(ctx, "go", cmdArgs...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(cmdArgs, " "), err, output)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	return filtered
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve dependency_direction_test.go location: runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
}
