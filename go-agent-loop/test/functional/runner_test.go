package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
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
		var report bytes.Buffer
		if err := applyPackageSelection(selection, &report); err != nil {
			t.Fatalf("filter %q: %v", tc.filter, err)
		}
		if got := runFlag.Value.String(); got != tc.want {
			t.Fatalf("filter %q: test.run = %q, want %q", tc.filter, got, tc.want)
		}
		if !strings.Contains(report.String(), "quarantine: selector=p/TestQuarantined ") {
			t.Fatalf("filter %q: quarantine report missing:\n%s", tc.filter, report.String())
		}
	}

	if err := flag.CommandLine.Set("test.run", "("); err != nil {
		t.Fatal(err)
	}
	if err := applyPackageSelection(Selection{Selected: selected}, io.Discard); err == nil || !strings.Contains(err.Error(), "compile existing test.run filter") {
		t.Fatalf("invalid -run filter: err = %v", err)
	}
}

// The package TestMain hook reads the external manifest, validates it
// against the real discovered inventory, and only then runs the package:
// a valid manifest narrows test.run and reports exact counts; an unknown
// selector fails closed before any test runs. RunPackageTests only adds
// flag parsing and os.Exit around this, so it runs in-process instead of
// through a recursive `go test`.
func TestRunSelectedPackageTestsAppliesExternalManifest(t *testing.T) {
	const orchestration = "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/orchestration"
	runFlag := flag.CommandLine.Lookup("test.run")
	original := runFlag.Value.String()
	t.Cleanup(func() {
		if err := flag.CommandLine.Set("test.run", original); err != nil {
			t.Errorf("restore test.run: %v", err)
		}
	})
	tests := []struct {
		name       string
		manifest   func(*testing.T) string
		runExit    int
		wantExit   int
		wantRun    bool
		wantFilter string
		wantStdout []string
		wantStderr string
	}{
		{
			name: "valid manifest narrows the run", manifest: writeProofManifest, wantRun: true,
			wantFilter: "^(?:TestBasic_SimpleRequestResponseWithSystemPrompt)$",
			wantStdout: []string{
				"quarantine: selector=" + orchestration + "/TestBasic_SimpleRequestResponse ",
				"summary: discovered=20 executed=19 passed=19 failed=0 quarantined=1",
			},
		},
		{
			name: "package failure is reported and propagated", manifest: writeProofManifest, runExit: 1, wantExit: 1, wantRun: true,
			wantFilter: "^(?:TestBasic_SimpleRequestResponseWithSystemPrompt)$",
			wantStdout: []string{"summary: discovered=20 executed=19 passed=0 failed=19 quarantined=1"},
		},
		{
			name: "unknown selector fails closed", manifest: writeUnknownSelectorManifest, wantExit: 1,
			wantFilter: "^TestBasic_(SimpleRequestResponse|SimpleRequestResponseWithSystemPrompt)$",
			wantStderr: "does not resolve to a discovered package",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(ManifestPathEnv, tc.manifest(t))
			if err := flag.CommandLine.Set("test.run", "^TestBasic_(SimpleRequestResponse|SimpleRequestResponseWithSystemPrompt)$"); err != nil {
				t.Fatal(err)
			}
			ran := false
			var stdout, stderr bytes.Buffer
			exit := runSelectedPackageTests(func() int { ran = true; return tc.runExit }, orchestration, &stdout, &stderr)
			if exit != tc.wantExit || ran != tc.wantRun {
				t.Fatalf("exit = %d ran = %v, want exit %d ran %v\nstdout:\n%s\nstderr:\n%s", exit, ran, tc.wantExit, tc.wantRun, stdout.String(), stderr.String())
			}
			if got := runFlag.Value.String(); got != tc.wantFilter {
				t.Fatalf("test.run = %q, want %q", got, tc.wantFilter)
			}
			assertOutputContains(t, "stdout", stdout.String(), tc.wantStdout...)
			assertOutputContains(t, "stderr", stderr.String(), tc.wantStderr)
		})
	}
}

func assertOutputContains(t *testing.T, stream, output string, fragments ...string) {
	t.Helper()
	for _, want := range fragments {
		if !strings.Contains(output, want) {
			t.Fatalf("%s missing %q:\n%s", stream, want, output)
		}
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
