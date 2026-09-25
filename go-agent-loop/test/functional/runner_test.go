package functional

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Discovery lists only concern packages under test/functional (not the root
// runner package, internal fixtures, or packages without tests) and their
// sorted top-level tests. The fixture is a standalone module in testdata.
func TestDiscoverFunctionalInventoryListsConcernPackagesAndTopLevelTests(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root, err := filepath.Abs(filepath.Join("testdata", "discovery"))
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := DiscoverFunctionalInventory(context.Background(), root)
	if err != nil {
		t.Fatalf("discover fixture inventory: %v", err)
	}
	want := Inventory{Packages: []InventoryPackage{
		{Path: "example.com/discovery/test/functional/alpha", Tests: []string{"TestAlphaOne", "TestAlphaTwo"}},
		{Path: "example.com/discovery/test/functional/beta", Tests: []string{"TestBeta"}},
	}}
	if !reflect.DeepEqual(inventory, want) {
		t.Fatalf("inventory = %+v, want %+v", inventory, want)
	}

	if _, err := DiscoverFunctionalInventory(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "discover functional packages") {
		t.Fatalf("discovery outside a module: err = %v, want a discover functional packages error", err)
	}
}

// A package TestMain narrows test.run to the manifest-selected tests that
// also match the caller's own -run filter, and reports each quarantine.
func TestApplyPackageSelectionIntersectsManifestWithRunFilter(t *testing.T) {
	runFlag := flag.CommandLine.Lookup("test.run")
	original := runFlag.Value.String()
	t.Cleanup(func() {
		if err := flag.CommandLine.Set("test.run", original); err != nil {
			t.Errorf("restore test.run: %v", err)
		}
	})
	selected := []TestSelector{{Package: "p", Test: "TestAlpha"}, {Package: "p", Test: "TestBeta"}}
	for _, tc := range []struct{ filter, want string }{
		{filter: "", want: "^(?:TestAlpha|TestBeta)$"},
		{filter: "Beta", want: "^(?:TestBeta)$"},
		{filter: "^TestGamma$", want: "a^"},
	} {
		if err := flag.CommandLine.Set("test.run", tc.filter); err != nil {
			t.Fatal(err)
		}
		selection := Selection{Selected: selected, Quarantined: []QuarantinedSelector{{
			Entry: Entry{Package: "p", Test: "TestQuarantined", Bucket: BucketGenuinelyFailing, Reason: "fixture", ExitCondition: "never"},
			Tests: []TestSelector{{Package: "p", Test: "TestQuarantined"}},
		}}}
		if err := applyPackageSelection(selection); err != nil {
			t.Fatalf("filter %q: %v", tc.filter, err)
		}
		if got := runFlag.Value.String(); got != tc.want {
			t.Fatalf("filter %q: test.run = %q, want %q", tc.filter, got, tc.want)
		}
	}

	if err := flag.CommandLine.Set("test.run", "("); err != nil {
		t.Fatal(err)
	}
	if err := applyPackageSelection(Selection{Selected: selected}); err == nil || !strings.Contains(err.Error(), "compile existing test.run filter") {
		t.Fatalf("invalid -run filter: err = %v", err)
	}
}

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
