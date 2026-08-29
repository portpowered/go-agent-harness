package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestErrorModes(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		check func(t *testing.T, err error)
	}{
		{
			name: "unregistered package",
			data: manifestJSON(`{"package":"example/a","minimum":0.00}`),
			check: func(t *testing.T, err error) {
				t.Helper()
				manifest := mustParseManifest(t, manifestJSON(`{"package":"example/a","minimum":0.00}`))
				got := Compare(manifest, map[string]Coverage{
					"example/a":       {Covered: 1, Total: 1},
					"example/missing": {Covered: 1, Total: 1},
				})
				if !errors.Is(got, ErrUnregisteredPackage) {
					t.Fatalf("errors.Is(%v, ErrUnregisteredPackage) = false", got)
				}
				want := "coverage gate found unregistered packages:\n- example/missing"
				if got.Error() != want {
					t.Fatalf("error = %q, want %q", got, want)
				}
			},
		},
		{
			name: "floor violation",
			data: manifestJSON(`{"package":"example/a","minimum":80.00}`),
			check: func(t *testing.T, err error) {
				t.Helper()
				manifest := mustParseManifest(t, manifestJSON(`{"package":"example/a","minimum":80.00}`))
				got := Compare(manifest, map[string]Coverage{"example/a": {Covered: 7, Total: 10}})
				if !errors.Is(got, ErrCoverageFloorViolation) {
					t.Fatalf("errors.Is(%v, ErrCoverageFloorViolation) = false", got)
				}
				want := "coverage gate found coverage floor violations:\n- example/a: expected minimum 80.00%, actual 70.00%, delta -10.00%"
				if got.Error() != want {
					t.Fatalf("error = %q, want %q", got, want)
				}
			},
		},
		{
			name: "malformed decimal precision",
			data: manifestJSON(`{"package":"example/a","minimum":80.0}`),
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, ErrManifestMinimumPrecision) {
					t.Fatalf("errors.Is(%v, ErrManifestMinimumPrecision) = false", err)
				}
				want := `coverage manifest package "example/a" minimum must use exactly two decimal places: got 80.0`
				if err.Error() != want {
					t.Fatalf("error = %q, want %q", err, want)
				}
			},
		},
		{
			name: "unsorted package array",
			data: `{"packages":[{"package":"example/z","minimum":0.00},{"package":"example/a","minimum":0.00}]}`,
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, ErrManifestUnsorted) {
					t.Fatalf("errors.Is(%v, ErrManifestUnsorted) = false", err)
				}
				want := `coverage manifest packages must be strictly sorted by import path: "example/a" follows "example/z"`
				if err.Error() != want {
					t.Fatalf("error = %q, want %q", err, want)
				}
			},
		},
		{
			name: "both minimum and exception",
			data: manifestJSON(`{"package":"example/a","minimum":0.00,"exception":"later"}`),
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, ErrManifestBothFields) {
					t.Fatalf("errors.Is(%v, ErrManifestBothFields) = false", err)
				}
				want := `coverage manifest package "example/a" must define exactly one of minimum or exception; found both`
				if err.Error() != want {
					t.Fatalf("error = %q, want %q", err, want)
				}
			},
		},
		{
			name: "neither minimum nor exception",
			data: manifestJSON(`{"package":"example/a"}`),
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, ErrManifestNeitherField) {
					t.Fatalf("errors.Is(%v, ErrManifestNeitherField) = false", err)
				}
				want := `coverage manifest package "example/a" must define exactly one of minimum or exception; found neither`
				if err.Error() != want {
					t.Fatalf("error = %q, want %q", err, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tt.data))
			if tt.name == "unregistered package" || tt.name == "floor violation" {
				if err != nil {
					t.Fatalf("ParseManifest() error = %v", err)
				}
				manifest := mustParseManifest(t, tt.data)
				if tt.name == "unregistered package" {
					err = Compare(manifest, map[string]Coverage{
						"example/a":       {Covered: 1, Total: 1},
						"example/missing": {Covered: 1, Total: 1},
					})
				} else {
					err = Compare(manifest, map[string]Coverage{"example/a": {Covered: 7, Total: 10}})
				}
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			tt.check(t, err)
		})
	}
}

func TestGoldenMultiPackageFailureReport(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/alpha","minimum":80.00},{"package":"example/zeta","minimum":90.00}]}`)
	err := Compare(manifest, map[string]Coverage{
		"example/zeta":  {Covered: 4, Total: 5},
		"example/alpha": {Covered: 3, Total: 5},
	})
	if err == nil {
		t.Fatal("Compare() returned nil for a violating measurement")
	}
	golden, readErr := os.ReadFile(filepath.Join("testdata", "multi-package-failure.golden"))
	if readErr != nil {
		t.Fatalf("read golden: %v", readErr)
	}
	if got, want := err.Error(), strings.TrimSpace(string(golden)); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("golden report error does not retain floor violation identity")
	}
}

func TestCompareReportsAllUnregisteredPackagesInOrder(t *testing.T) {
	manifest := mustParseManifest(t, manifestJSON(`{"package":"example/registered","minimum":0.00}`))
	err := Compare(manifest, map[string]Coverage{
		"example/registered": {Covered: 1, Total: 1},
		"example/zeta":       {Covered: 1, Total: 1},
		"example/alpha":      {Covered: 1, Total: 1},
	})
	if err == nil {
		t.Fatal("Compare() returned nil for unregistered packages")
	}
	want := "coverage gate found unregistered packages:\n- example/alpha\n- example/zeta"
	if got := err.Error(); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrUnregisteredPackage) {
		t.Fatal("missing-package report lost its typed error identity")
	}
}

func TestCompareAllowsCoverageQuantizationBandWithoutChangingFloor(t *testing.T) {
	tests := []struct {
		name      string
		minimum   string
		wantDelta int
	}{
		{name: "half band", minimum: "80.05", wantDelta: -5},
		{name: "exact band", minimum: "80.10", wantDelta: -10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := mustParseManifest(t, manifestJSON(fmt.Sprintf(`{"package":"example/a","minimum":%s}`, tt.minimum)))
			if err := Compare(manifest, map[string]Coverage{"example/a": {Covered: 8, Total: 10}}); err != nil {
				t.Fatalf("Compare() rejected a shortfall within the 0.10-point band: %v", err)
			}
			if got, want := manifest.Packages[0].MinimumCents, 8000+(-tt.wantDelta); got != want {
				t.Fatalf("stored minimum = %d cents, want %d cents", got, want)
			}
		})
	}
}

func TestCompareRejectsCoverageBeyondQuantizationBand(t *testing.T) {
	manifest := mustParseManifest(t, manifestJSON(`{"package":"example/a","minimum":80.50}`))
	err := Compare(manifest, map[string]Coverage{"example/a": {Covered: 8, Total: 10}})
	if err == nil {
		t.Fatal("Compare() accepted a 0.50-point floor regression")
	}
	if !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("errors.Is(%v, ErrCoverageFloorViolation) = false", err)
	}
	want := "coverage gate found coverage floor violations:\n- example/a: expected minimum 80.50%, actual 80.00%, delta -0.50%"
	if got := err.Error(); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
	if got, want := manifest.Packages[0].MinimumCents, 8050; got != want {
		t.Fatalf("stored minimum = %d cents, want %d cents", got, want)
	}
}

func TestLoadManifestDirMergesIndependentFragmentsInPackageOrder(t *testing.T) {
	directory := t.TempDir()
	writeManifestFragment(t, filepath.Join(directory, "nested", "zeta.fragment"), `{"package":"example/zeta","minimum":50.00}`)
	writeManifestFragment(t, filepath.Join(directory, "alpha.fragment"), `{"package":"example/alpha","minimum":80.00}`)

	manifest, err := LoadManifestDir(directory)
	if err != nil {
		t.Fatalf("LoadManifestDir() error = %v", err)
	}
	if got, want := len(manifest.Packages), 2; got != want {
		t.Fatalf("registered package count = %d, want %d", got, want)
	}
	if got, want := manifest.Packages[0].ImportPath, "example/alpha"; got != want {
		t.Fatalf("first package = %q, want %q", got, want)
	}
	if got, want := manifest.Packages[1].ImportPath, "example/zeta"; got != want {
		t.Fatalf("second package = %q, want %q", got, want)
	}

	err = Compare(manifest, map[string]Coverage{
		"example/alpha": {Covered: 7, Total: 10},
		"example/zeta":  {Covered: 4, Total: 10},
	})
	if err == nil {
		t.Fatal("Compare() succeeded with both fragment floors below their minimum")
	}
	if !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("errors.Is(%v, ErrCoverageFloorViolation) = false", err)
	}
	want := "coverage gate found coverage floor violations:\n- example/alpha: expected minimum 80.00%, actual 70.00%, delta -10.00%\n- example/zeta: expected minimum 50.00%, actual 40.00%, delta -10.00%"
	if got := err.Error(); got != want {
		t.Fatalf("report = %q, want %q", got, want)
	}
}

func TestLoadManifestDirReportsDuplicateFragments(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.fragment")
	second := filepath.Join(directory, "second.fragment")
	writeManifestFragment(t, first, `{"package":"example/duplicate","minimum":40.00}`)
	writeManifestFragment(t, second, `{"package":"example/duplicate","exception":"tracked separately"}`)

	_, err := LoadManifestDir(directory)
	if err == nil {
		t.Fatal("LoadManifestDir() succeeded with duplicate package fragments")
	}
	if !errors.Is(err, ErrManifestDuplicate) {
		t.Fatalf("errors.Is(%v, ErrManifestDuplicate) = false", err)
	}
	for _, want := range []string{"example/duplicate", first, second} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("duplicate error = %q, want it to contain %q", err, want)
		}
	}
}

func TestLoadManifestDirReportsMalformedFragmentPath(t *testing.T) {
	directory := t.TempDir()
	fragment := filepath.Join(directory, "broken.fragment")
	writeManifestFragment(t, fragment, `{"package":"example/broken","minimum":80.0}`)

	_, err := LoadManifestDir(directory)
	if err == nil {
		t.Fatal("LoadManifestDir() succeeded with malformed fragment")
	}
	if !errors.Is(err, ErrManifestMinimumPrecision) {
		t.Fatalf("errors.Is(%v, ErrManifestMinimumPrecision) = false", err)
	}
	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("malformed fragment error = %q, want it to contain %q", err, fragment)
	}
}

func TestRepositoryFragmentCatalogMatchesPreMigrationBaseline(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pre-migration-coverage-manifest.json"))
	if err != nil {
		t.Fatalf("read pre-migration baseline: %v", err)
	}
	want, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	got := mustLoadRepositoryManifest(t)
	if len(got.Packages) != len(want.Packages) {
		t.Fatalf("registered package count = %d, want %d", len(got.Packages), len(want.Packages))
	}
	for i := range want.Packages {
		if got.Packages[i] != want.Packages[i] {
			t.Fatalf("package registration %d = %#v, want %#v", i, got.Packages[i], want.Packages[i])
		}
	}
}

func TestRepositoryFragmentCatalogPreservesCoverageEnforcement(t *testing.T) {
	manifest := mustLoadRepositoryManifest(t)
	measurements := allPackagesMeasured(manifest)
	if err := Compare(manifest, measurements); err != nil {
		t.Fatalf("non-regressing repository measurements rejected: %v", err)
	}

	withUnregistered := cloneMeasurements(measurements)
	const unregisteredPackage = "github.com/portpowered/go-agent-harness/new/package"
	withUnregistered[unregisteredPackage] = Coverage{Covered: 1, Total: 1}
	err := Compare(manifest, withUnregistered)
	if !errors.Is(err, ErrUnregisteredPackage) {
		t.Fatalf("errors.Is(%v, ErrUnregisteredPackage) = false", err)
	}
	if !strings.Contains(err.Error(), unregisteredPackage) {
		t.Fatalf("unregistered-package report = %q, want %q", err, unregisteredPackage)
	}

	const targetPackage = "github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	withoutMeasurement := cloneMeasurements(measurements)
	delete(withoutMeasurement, targetPackage)
	err = Compare(manifest, withoutMeasurement)
	if !errors.Is(err, ErrMissingCoverage) {
		t.Fatalf("errors.Is(%v, ErrMissingCoverage) = false", err)
	}
	if !strings.Contains(err.Error(), targetPackage) {
		t.Fatalf("missing-coverage report = %q, want %q", err, targetPackage)
	}

	withRegression := cloneMeasurements(measurements)
	withRegression[targetPackage] = Coverage{Covered: 397, Total: 1000}
	err = Compare(manifest, withRegression)
	if !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("errors.Is(%v, ErrCoverageFloorViolation) = false", err)
	}
	var findings *FindingsError
	if !errors.As(err, &findings) {
		t.Fatalf("coverage regression error %T does not expose FindingsError", err)
	}
	wantViolation := Violation{ImportPath: targetPackage, ExpectedCents: 4020, ActualCents: 3970, DeltaCents: -50}
	if len(findings.Violations) != 1 || findings.Violations[0] != wantViolation {
		t.Fatalf("coverage violations = %#v, want %#v", findings.Violations, []Violation{wantViolation})
	}
}

func TestKnownGoodManifestAndProfiles(t *testing.T) {
	manifest := mustLoadRepositoryManifest(t)
	if len(manifest.Packages) == 0 {
		t.Fatal("manifest contains no packages")
	}
	hasZeroMinimum := false
	hasPositiveMinimum := false
	for _, entry := range manifest.Packages {
		if entry.ImportPath == "" || !entry.HasMinimum || entry.HasException {
			t.Fatalf("entry %q is not a minimum registration: %#v", entry.ImportPath, entry)
		}
		if entry.MinimumCents == 0 {
			hasZeroMinimum = true
		} else if entry.MinimumCents > 0 {
			hasPositiveMinimum = true
		}
	}
	if !hasZeroMinimum {
		t.Fatal("manifest contains no zero-coverage package")
	}
	if !hasPositiveMinimum {
		t.Fatal("manifest contains no positive coverage package")
	}
	measurements := make(map[string]Coverage, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		measurements[entry.ImportPath] = Coverage{Covered: 1, Total: 1}
	}
	if err := Compare(manifest, measurements); err != nil {
		t.Fatalf("known-good committed manifest rejected: %v", err)
	}
}

func TestReadProfilesAggregatesPackages(t *testing.T) {
	first := writeProfile(t, "mode: set\nexample/a/a.go:1.1,1.2 2 1\nexample/a/a.go:2.1,2.2 3 0\n")
	second := writeProfile(t, "mode: set\nexample/a/b.go:1.1,1.2 5 1\nexample/b/b.go:1.1,1.2 1 0\n")
	measurements, err := ReadProfiles([]string{first, second})
	if err != nil {
		t.Fatalf("ReadProfiles() error = %v", err)
	}
	if got, want := len(measurements), 2; got != want {
		t.Fatalf("measured package count = %d, want %d", got, want)
	}
	if got, want := measurements["example/a"], (Coverage{Covered: 7, Total: 10}); got != want {
		t.Fatalf("example/a coverage = %#v, want %#v", got, want)
	}
	if got := measurements["example/b"]; got != (Coverage{Total: 1}) {
		t.Fatalf("example/b coverage = %#v, want 0/1", got)
	}
}

func TestCommandFailureExitsNonZero(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest")
	profilePath := filepath.Join(directory, "profile.out")
	writeManifestFragment(t, filepath.Join(manifestPath, "example-a.json"), `{"package":"example/a","minimum":80.00}`)
	if err := os.WriteFile(profilePath, []byte("mode: set\nexample/a/a.go:1.1,1.2 10 0\n"), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	command := exec.Command("go", "run", ".", "--manifest", manifestPath, profilePath)
	command.Dir = "."
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("command succeeded; output = %s", output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("command error = %v, want non-zero exit", err)
	}
	if !strings.Contains(string(output), "example/a: expected minimum 80.00%, actual 0.00%, delta -80.00%") {
		t.Fatalf("command output = %q, want actionable floor diagnostic", output)
	}
}

func mustLoadRepositoryManifest(t *testing.T) Manifest {
	t.Helper()
	manifest, err := LoadManifestDir(filepath.Join("..", "..", "coverage-manifest"))
	if err != nil {
		t.Fatalf("LoadManifestDir() error = %v", err)
	}
	return manifest
}

func allPackagesMeasured(manifest Manifest) map[string]Coverage {
	measurements := make(map[string]Coverage, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		measurements[entry.ImportPath] = Coverage{Covered: 1, Total: 1}
	}
	return measurements
}

func cloneMeasurements(measurements map[string]Coverage) map[string]Coverage {
	clone := make(map[string]Coverage, len(measurements))
	for packagePath, coverage := range measurements {
		clone[packagePath] = coverage
	}
	return clone
}

func manifestJSON(entry string) string {
	return fmt.Sprintf(`{"packages":[%s]}`, entry)
}

func mustParseManifest(t *testing.T, data string) Manifest {
	t.Helper()
	manifest, err := ParseManifest([]byte(data))
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	return manifest
}

func writeProfile(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

func writeManifestFragment(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir fragment directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write manifest fragment: %v", err)
	}
}
