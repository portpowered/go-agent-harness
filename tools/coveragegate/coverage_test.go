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
		name string
		data string
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

func TestKnownGoodManifestAndProfiles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "coverage-manifest.json"))
	if err != nil {
		t.Fatalf("read committed manifest: %v", err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	if got, want := len(manifest.Packages), 43; got != want {
		t.Fatalf("manifest package count = %d, want %d", got, want)
	}
	zeroCount := 0
	for _, entry := range manifest.Packages {
		if !entry.HasMinimum || entry.HasException {
			t.Fatalf("entry %q is not a minimum registration: %#v", entry.ImportPath, entry)
		}
		if entry.MinimumCents == 0 {
			zeroCount++
		}
	}
	if zeroCount != 12 {
		t.Fatalf("zero-coverage package count = %d, want 12", zeroCount)
	}
	measurements := make(map[string]Coverage, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		measurements[entry.ImportPath] = Coverage{Covered: 1, Total: 1}
	}
	if err := Compare(Manifest{Packages: []PackageEntry{{ImportPath: "example/a", MinimumCents: 0, HasMinimum: true}}}, map[string]Coverage{"example/a": {Covered: 1, Total: 1}}); err != nil {
		t.Fatalf("known-good synthetic manifest rejected: %v", err)
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
	manifestPath := filepath.Join(directory, "manifest.json")
	profilePath := filepath.Join(directory, "profile.out")
	if err := os.WriteFile(manifestPath, []byte(manifestJSON(`{"package":"example/a","minimum":80.00}`)), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
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
