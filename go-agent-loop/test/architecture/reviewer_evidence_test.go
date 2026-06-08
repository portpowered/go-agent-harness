package architecture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhase3SharedContractReviewerEvidence(t *testing.T) {
	root := repoRoot(t)

	t.Run("package docs expose the authoritative shared boundary", func(t *testing.T) {
		output := goDocOutput(t, root, "github.com/portpowered/go-agent-loop/pkg/messages")
		assertContainsAll(t, output,
			"authoritative shared runtime contracts",
			"conversation messages",
			"streaming events",
			"tool payloads",
			"token-usage",
			"session interfaces",
		)
	})

	t.Run("adapter docs expose the bridge role", func(t *testing.T) {
		output := goDocOutput(t, root, "github.com/portpowered/go-llm-gateway/pkg/inference")
		assertContainsAll(t, output,
			"public bridge from go-llm-gateway",
			"authoritative loop-owned contracts",
			"GatewayInferencer adapts stateless gateway inference to messages.Inferencer",
			"SessionGatewayInferencer adapts gateway session establishment",
		)
	})

	t.Run("reviewer closure surfaces cite the same evidence set", func(t *testing.T) {
		checklist := readRepoFile(t, root, "docs/internal/checklist.md")
		assertContainsAll(t, checklist,
			"`P3-CORE-01`",
			"`P3-CORE-02`",
			"`go-agent-loop/test/architecture/reviewer_evidence_test.go`",
			"`go-agent-loop/test/architecture/dependency_direction_test.go`",
		)

		audit := readRepoFile(t, root, "docs/architecture/contract-gap-audit.md")
		assertContainsAll(t, audit,
			"reviewer-facing evidence checks",
			"`go-agent-loop/test/architecture/reviewer_evidence_test.go`",
			"`go-agent-loop/test/architecture/dependency_direction_test.go`",
		)
	})
}

func goDocOutput(t *testing.T, dir string, pkg string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "doc", pkg)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go doc %s failed: %v\n%s", pkg, err, output)
	}
	return string(output)
}

func readRepoFile(t *testing.T, root string, rel string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(content)
}

func assertContainsAll(t *testing.T, got string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain %q\nfull output:\n%s", want, got)
		}
	}
}
