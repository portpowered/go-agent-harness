package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverWorkspacePackagesListsEveryModuleDeterministically(t *testing.T) {
	first := writeRegistrationModule(t, "example.test/first", map[string]string{
		"root.go":          "package first\n",
		"nested/nested.go": "package nested\n",
	})
	second := writeRegistrationModule(t, "example.test/second", map[string]string{
		"root.go": "package second\n",
	})

	got, err := DiscoverWorkspacePackages(context.Background(), "go", []string{second, first})
	if err != nil {
		t.Fatalf("DiscoverWorkspacePackages() error = %v", err)
	}
	want := []string{
		"example.test/first",
		"example.test/first/nested",
		"example.test/second",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("discovered packages = %v, want %v", got, want)
	}
}

func TestValidateRegistrationAcceptsExactSetInAnyDiscoveryOrder(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/a","minimum":0.00},{"package":"example/b","exception":"generated"}]}`)
	if err := ValidateRegistration(manifest, []string{"example/b", "example/a"}); err != nil {
		t.Fatalf("ValidateRegistration() error = %v", err)
	}
}

func TestValidateRegistrationReportsMissingAndStalePackages(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/a","minimum":0.00},{"package":"example/stale","minimum":0.00}]}`)
	err := ValidateRegistration(manifest, []string{"example/new", "example/a"})
	if err == nil {
		t.Fatal("ValidateRegistration() succeeded for a mismatched package set")
	}
	if !errors.Is(err, ErrRegistrationMissing) || !errors.Is(err, ErrRegistrationStale) {
		t.Fatalf("registration error = %v, want missing and stale identities", err)
	}
	var registrationErr *RegistrationError
	if !errors.As(err, &registrationErr) {
		t.Fatalf("registration error type = %T, want *RegistrationError", err)
	}
	if fmt.Sprint(registrationErr.Missing) != "[example/new]" {
		t.Fatalf("missing packages = %v", registrationErr.Missing)
	}
	if fmt.Sprint(registrationErr.Stale) != "[example/stale]" {
		t.Fatalf("stale packages = %v", registrationErr.Stale)
	}
	message := err.Error()
	for _, expected := range []string{"example/new", "example/stale", "update coverage-manifest"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("registration error = %q, want substring %q", message, expected)
		}
	}
}

func TestValidateRegistrationRejectsDuplicateManifestEntry(t *testing.T) {
	_, err := ParseManifest([]byte(`{"packages":[{"package":"example/a","minimum":0.00},{"package":"example/a","minimum":0.00}]}`))
	if err == nil {
		t.Fatal("ParseManifest() accepted a duplicate package")
	}
	if !errors.Is(err, ErrManifestDuplicate) {
		t.Fatalf("duplicate manifest error = %v, want ErrManifestDuplicate", err)
	}
	if !strings.Contains(err.Error(), `"example/a"`) {
		t.Fatalf("duplicate manifest error = %q, want offending package", err)
	}
}

func TestRunValidateRegistrationDoesNotRunCoverageTests(t *testing.T) {
	moduleDir := t.TempDir()
	manifestPath := writeRegistrationManifest(t, `{"packages":[{"package":"example.test/pkg","minimum":0.00}]}`)
	fakeGo := filepath.Join(t.TempDir(), "go")
	fakeScript := `#!/bin/sh
for argument in "$@"; do
  if [ "$argument" = "test" ]; then
    echo "unexpected go test invocation" >&2
    exit 91
  fi
done
printf '%s\n' 'example.test/pkg'
`
	if err := os.WriteFile(fakeGo, []byte(fakeScript), 0o700); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	var stdout, stderr strings.Builder
	err := run([]string{
		"--validate-registration",
		"--manifest", manifestPath,
		"--go", fakeGo,
		"--module-dir", moduleDir,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("registration run error = %v (stderr %q)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "coverage registration passed: 1 workspace packages checked across 1 modules") {
		t.Fatalf("registration output = %q", stdout.String())
	}
}

func TestRunValidateRegistrationRejectsManifestOrder(t *testing.T) {
	manifestPath := writeRegistrationManifest(t, `{"packages":[{"package":"example/z","minimum":0.00},{"package":"example/a","minimum":0.00}]}`)
	var stdout, stderr strings.Builder
	err := run([]string{
		"--validate-registration",
		"--manifest", manifestPath,
		"--go", "does-not-need-to-run",
		"--module-dir", t.TempDir(),
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("registration run accepted an unsorted manifest")
	}
	if !errors.Is(err, ErrManifestUnsorted) {
		t.Fatalf("manifest order error = %v, want ErrManifestUnsorted", err)
	}
	if !strings.Contains(err.Error(), `"example/a" follows "example/z"`) {
		t.Fatalf("manifest order error = %q", err)
	}
}

func writeRegistrationModule(t *testing.T, modulePath string, files map[string]string) string {
	t.Helper()
	directory := t.TempDir()
	writeRegistrationFile(t, directory, "go.mod", fmt.Sprintf("module %s\n\ngo 1.26.7\n", modulePath))
	for path, contents := range files {
		writeRegistrationFile(t, directory, path, contents)
	}
	return directory
}

func writeRegistrationManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coverage-manifest.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write registration manifest: %v", err)
	}
	return path
}

func writeRegistrationFile(t *testing.T, directory, path, contents string) {
	t.Helper()
	filePath := filepath.Join(directory, path)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(filePath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
