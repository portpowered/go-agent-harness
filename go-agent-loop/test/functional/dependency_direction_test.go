package functional

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	gatewayModuleImportPath = "github.com/portpowered/go-llm-gateway"
	loopModuleDir           = "../.."
)

func TestDependencyDirection_GoAgentLoopDoesNotDependOnGateway(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...")
	cmd.Dir = loopModuleDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list deps for go-agent-loop: %v\n%s", err, output)
	}

	for _, dep := range strings.Fields(string(output)) {
		if dep == gatewayModuleImportPath || strings.HasPrefix(dep, gatewayModuleImportPath+"/") {
			t.Fatalf("go-agent-loop reverse dependency drift: found forbidden dependency %q in `go list -deps ./...` output", dep)
		}
	}
}
