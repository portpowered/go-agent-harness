package functional

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFunctionalQuarantine_EmptyManifestRunsEveryDiscoveredTest(t *testing.T) {
	manifest, err := ReadEmbeddedManifest()
	if err != nil {
		t.Fatalf("read embedded manifest: %v", err)
	}
	inventory := Inventory{Packages: []InventoryPackage{{Path: "fixture/package", Tests: []string{"TestAlpha", "TestBeta"}}}}
	var ran []string
	var output bytes.Buffer
	report, err := Run(context.Background(), manifest, inventory, func(_ context.Context, selector TestSelector) error {
		ran = append(ran, selector.String())
		return nil
	}, &output)
	if err != nil {
		t.Fatalf("run empty manifest: %v", err)
	}
	if got, want := strings.Join(ran, ","), "fixture/package/TestAlpha,fixture/package/TestBeta"; got != want {
		t.Fatalf("runnable selectors: got %q, want %q", got, want)
	}
	if report.Discovered != 2 || report.Executed != 2 || report.Passed != 2 || report.Failed != 0 || report.Quarantined != 0 {
		t.Fatalf("report counts: %+v", report)
	}
	if !strings.Contains(output.String(), "summary: discovered=2 executed=2 passed=2 failed=0 quarantined=0") {
		t.Fatalf("summary missing exact counts:\n%s", output.String())
	}
}

func TestFunctionalQuarantine_ExactSelectorSkipsFailingInjectedSubprocess(t *testing.T) {
	const packagePath = "fixture/package"
	manifest := Manifest{
		Version: ManifestVersion,
		Suite:   SuiteName,
		Entries: []Entry{{
			Package:       packagePath,
			Test:          "TestQuarantined",
			Bucket:        BucketGenuinelyFailing,
			Reason:        "the injected fixture is intentionally failing",
			ExitCondition: "remove after the fixture exits successfully",
		}},
	}
	inventory := Inventory{Packages: []InventoryPackage{{Path: packagePath, Tests: []string{"TestRunnable", "TestQuarantined"}}}}
	moduleRoot := functionalModuleRoot(t)
	var fixtureOutput bytes.Buffer
	var invoked []string
	var reportOutput bytes.Buffer
	report, err := RunSubprocess(context.Background(), manifest, inventory, func(ctx context.Context, selector TestSelector) *exec.Cmd {
		invoked = append(invoked, selector.String())
		cmd := exec.CommandContext(ctx, "go", "run", "./test/functional/internal/quarantinefixture")
		cmd.Dir = moduleRoot
		cmd.Env = append(os.Environ(), "FUNCTIONAL_FIXTURE_SELECTOR="+selector.String())
		if selector.Test == "TestQuarantined" {
			cmd.Env = append(cmd.Env, "FUNCTIONAL_FIXTURE_FAIL=1")
		}
		cmd.Stdout = &fixtureOutput
		cmd.Stderr = &fixtureOutput
		return cmd
	}, &reportOutput)
	if err != nil {
		t.Fatalf("run injected subprocesses: %v\n%s", err, reportOutput.String())
	}
	if got, want := strings.Join(invoked, ","), packagePath+"/TestRunnable"; got != want {
		t.Fatalf("executed selectors: got %q, want only %q", got, want)
	}
	if !strings.Contains(fixtureOutput.String(), "fixture-ran selector="+packagePath+"/TestRunnable") {
		t.Fatalf("positive fixture signal missing:\n%s", fixtureOutput.String())
	}
	if strings.Contains(fixtureOutput.String(), "quarantine-sentinel-executed") {
		t.Fatal("quarantined failure sentinel executed")
	}
	if report.Discovered != 2 || report.Executed != 1 || report.Passed != 1 || report.Failed != 0 || report.Quarantined != 1 || report.QuarantineEntryCount != 1 {
		t.Fatalf("report counts: %+v", report)
	}
	for _, expected := range []string{
		"quarantine: selector=" + packagePath + "/TestQuarantined",
		"reason=\"the injected fixture is intentionally failing\"",
		"summary: discovered=2 executed=1 passed=1 failed=0 quarantined=1",
	} {
		if !strings.Contains(reportOutput.String(), expected) {
			t.Fatalf("report missing %q:\n%s", expected, reportOutput.String())
		}
	}
}

func TestFunctionalQuarantine_RejectsMalformedIncompleteDuplicateAmbiguousAndUnknownSelectors(t *testing.T) {
	validInventory := Inventory{Packages: []InventoryPackage{{Path: "fixture/package", Tests: []string{"TestKnown"}}}}
	tests := []struct {
		name       string
		manifest   string
		value      Manifest
		inventory  Inventory
		wantField  string
		wantSelect string
	}{
		{
			name:      "malformed json",
			manifest:  `{"version":1,`,
			wantField: "document",
		},
		{
			name:      "missing entries",
			manifest:  `{"version":1,"suite":"functional"}`,
			wantField: "entries",
		},
		{
			name:      "unknown field",
			manifest:  `{"version":1,"suite":"functional","entries":[],"unexpected":true}`,
			wantField: "document",
		},
		{
			name:      "missing exit condition",
			manifest:  `{"version":1,"suite":"functional","entries":[{"package":"fixture/package","test":"TestKnown","bucket":"ENVIRONMENT-DEPENDENT","reason":"needs setup"}]}`,
			wantField: "entries[0].exitCondition",
		},
		{
			name:       "unknown package",
			value:      Manifest{Version: ManifestVersion, Suite: SuiteName, Entries: []Entry{{Package: "fixture/missing", Test: "TestKnown", Bucket: BucketEnvironmentDependent, Reason: "not ready", ExitCondition: "restore the fixture"}}},
			inventory:  validInventory,
			wantField:  "entries[0].package",
			wantSelect: "fixture/missing/TestKnown",
		},
		{
			name: "duplicate selector",
			value: Manifest{Version: ManifestVersion, Suite: SuiteName, Entries: []Entry{
				{Package: "fixture/package", Test: "TestKnown", Bucket: BucketEnvironmentDependent, Reason: "one", ExitCondition: "two"},
				{Package: "fixture/package", Test: "TestKnown", Bucket: BucketEnvironmentDependent, Reason: "three", ExitCondition: "four"},
			}},
			inventory:  validInventory,
			wantField:  "entries[1]",
			wantSelect: "fixture/package/TestKnown",
		},
		{
			name: "overlapping package and test selectors",
			value: Manifest{Version: ManifestVersion, Suite: SuiteName, Entries: []Entry{
				{Package: "fixture/package", Bucket: BucketEnvironmentDependent, Reason: "whole package", ExitCondition: "restore the package"},
				{Package: "fixture/package", Test: "TestKnown", Bucket: BucketEnvironmentDependent, Reason: "same package", ExitCondition: "restore the test"},
			}},
			inventory:  validInventory,
			wantField:  "entries[1]",
			wantSelect: "fixture/package/TestKnown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.manifest != "" {
				_, err = ParseManifest([]byte(tc.manifest))
			} else {
				_, err = Select(tc.value, tc.inventory)
			}
			if err == nil {
				t.Fatal("expected fail-closed validation error")
			}
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type: %T (%v), want *ValidationError", err, err)
			}
			if validationErr.Field != tc.wantField {
				t.Errorf("error field: got %q, want %q", validationErr.Field, tc.wantField)
			}
			if tc.wantSelect != "" && validationErr.Selector != tc.wantSelect {
				t.Errorf("error selector: got %q, want %q", validationErr.Selector, tc.wantSelect)
			}
		})
	}
}

func functionalModuleRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}
