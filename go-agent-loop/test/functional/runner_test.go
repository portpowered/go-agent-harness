package functional

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFunctionalSuite_ExternalManifestControlsRecursiveInvocation(t *testing.T) {
	// Each subprocess reads its own temporary manifest, so the two
	// recursive invocations are independent.
	t.Parallel()
	moduleRoot, err := functionalModuleRootPath()
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := writeProofManifest(t)
	cmd := exec.Command("go", goCommandArgs(
		"test",
		"./test/functional/...",
		"-v",
		"-run", "^TestBasic_(SimpleRequestResponse|SimpleRequestResponseWithSystemPrompt)$",
		"-count=1",
	)...)
	cmd.Dir = moduleRoot
	cmd.Env = setEnv(os.Environ(), ManifestPathEnv, manifestPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("recursive invocation: %v\n%s", err, output)
	}

	text := string(output)
	quarantined := "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/orchestration/TestBasic_SimpleRequestResponse"
	if !strings.Contains(text, "quarantine: selector="+quarantined+" ") {
		t.Fatalf("recursive invocation did not report the quarantined real selector:\n%s", text)
	}
	if strings.Contains(text, "--- PASS: TestBasic_SimpleRequestResponse (") {
		t.Fatalf("recursive invocation executed the quarantined selector:\n%s", text)
	}
	if !strings.Contains(text, "--- PASS: TestBasic_SimpleRequestResponseWithSystemPrompt (") {
		t.Fatalf("recursive invocation did not execute the runnable real selector:\n%s", text)
	}
	if !strings.Contains(text, "summary: discovered=20 executed=19 passed=19 failed=0 quarantined=1") {
		t.Fatalf("recursive invocation did not report exact package counts:\n%s", text)
	}
}

func TestFunctionalSuite_ExternalManifestRejectsUnknownSelectorBeforeFilteredRecursiveInvocation(t *testing.T) {
	// Each subprocess reads its own temporary manifest, so the two
	// recursive invocations are independent.
	t.Parallel()
	moduleRoot, err := functionalModuleRootPath()
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := writeUnknownSelectorManifest(t)
	cmd := exec.Command("go", goCommandArgs(
		"test",
		"./test/functional/...",
		"-v",
		"-run", "^TestBasic_SimpleRequestResponse$",
		"-count=1",
	)...)
	cmd.Dir = moduleRoot
	cmd.Env = setEnv(os.Environ(), ManifestPathEnv, manifestPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("recursive invocation accepted an unknown selector:\n%s", output)
	}

	text := string(output)
	if !strings.Contains(text, "does not resolve to a discovered package") {
		t.Fatalf("recursive invocation did not report the typed unknown-selector error:\n%s", text)
	}
	if strings.Contains(text, "--- PASS: TestBasic_SimpleRequestResponse (") {
		t.Fatalf("recursive invocation ran a test despite the invalid manifest:\n%s", text)
	}
}

func writeProofManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "functional-quarantine.json")
	body, err := json.MarshalIndent(Manifest{
		Version: ManifestVersion,
		Suite:   SuiteName,
		Entries: []Entry{
			{
				Package:       "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/media",
				Bucket:        BucketEnvironmentDependent,
				Reason:        "canonical runner proof excludes media",
				ExitCondition: "remove when the runner proof no longer needs package quarantine",
			},
			{
				Package:       "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/sessions",
				Bucket:        BucketEnvironmentDependent,
				Reason:        "canonical runner proof excludes sessions",
				ExitCondition: "remove when the runner proof no longer needs package quarantine",
			},
			{
				Package:       "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/tools",
				Bucket:        BucketEnvironmentDependent,
				Reason:        "canonical runner proof excludes tools",
				ExitCondition: "remove when the runner proof no longer needs package quarantine",
			},
			{
				Package:       "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/orchestration",
				Test:          "TestBasic_SimpleRequestResponse",
				Bucket:        BucketGenuinelyFailing,
				Reason:        "canonical runner proof excludes one real test",
				ExitCondition: "remove when the canonical runner proof is retired",
			},
		},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal external manifest: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write external manifest: %v", err)
	}
	return path
}

func writeUnknownSelectorManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "functional-quarantine.json")
	body, err := json.MarshalIndent(Manifest{
		Version: ManifestVersion,
		Suite:   SuiteName,
		Entries: []Entry{{
			Package:       "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/typo",
			Test:          "TestMissing",
			Bucket:        BucketGenuinelyFailing,
			Reason:        "the selector is intentionally unknown for the fail-closed proof",
			ExitCondition: "remove after the proof no longer needs an invalid manifest",
		}},
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal unknown-selector manifest: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write unknown-selector manifest: %v", err)
	}
	return path
}
