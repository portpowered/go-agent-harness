package dependencyproof

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	loopRuntimePackagePrefix = "github.com/portpowered/go-agent-loop/pkg/"
	allowedSharedContract    = "github.com/portpowered/go-agent-loop/pkg/messages"
)

var phase3ScopedGatewayPackages = []string{
	"./pkg/gateway",
	"./pkg/logging",
	"./pkg/providers/openai",
	"./pkg/providers/grok",
}

func TestPhase3GatewayRuntimeBoundary_AllowsMessagesButRejectsLoopOwnedNonContractPackages(t *testing.T) {
	cmd := exec.Command("go", append([]string{
		"list",
		"-deps",
		"-test",
		"-f",
		"{{if not .Standard}}{{.ImportPath}}{{end}}",
	}, phase3ScopedGatewayPackages...)...)
	cmd.Dir = moduleRootFromHere()

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list dependency proof for Phase 3 gateway runtime boundary failed: %v\n%s", err, output)
	}

	var forbidden []string
	for _, dep := range strings.Fields(string(output)) {
		if dep == allowedSharedContract {
			continue
		}
		if strings.HasPrefix(dep, loopRuntimePackagePrefix) {
			forbidden = append(forbidden, dep)
		}
	}

	forbidden = slices.Compact(forbidden)
	if len(forbidden) != 0 {
		t.Fatalf(
			"Phase 3 dependency proof allows %q only and rejects loop-owned non-contract runtime packages in %v; found forbidden dependencies: %s",
			allowedSharedContract,
			phase3ScopedGatewayPackages,
			strings.Join(forbidden, ", "),
		)
	}
}

func moduleRootFromHere() string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return ".."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../.."))
}
