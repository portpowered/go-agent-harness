package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectChangedPackagesNoChangesDoesNotDiscoverPackages(t *testing.T) {
	repo, modules, base := newChangedPackageRepository(t)
	selection, err := SelectChangedPackages(
		context.Background(),
		"git",
		filepath.Join(t.TempDir(), "go-must-not-run"),
		repo,
		base,
		modules,
	)
	if err != nil {
		t.Fatalf("SelectChangedPackages() error = %v", err)
	}
	if len(selection.GoFiles) != 0 || len(selection.Packages) != 0 || len(selection.UnownedGoFiles) != 0 {
		t.Fatalf("no-change selection = %#v, want empty result", selection)
	}
}

func TestSelectChangedPackagesIncludesAllChangeSourcesAndMapsCurrentPackages(t *testing.T) {
	repo, modules, base := newChangedPackageRepository(t)

	if err := os.Rename(filepath.Join(modules[0], "a.go"), filepath.Join(modules[0], "renamed.go")); err != nil {
		t.Fatalf("rename committed package file: %v", err)
	}
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-qm", "rename package file")

	if err := os.Remove(filepath.Join(modules[1], "b.go")); err != nil {
		t.Fatalf("delete staged package file: %v", err)
	}
	if err := os.Remove(filepath.Join(modules[0], "gone", "gone.go")); err != nil {
		t.Fatalf("delete removed package file: %v", err)
	}
	runTestGit(t, repo, "add", "-u")

	appendTestFile(t, filepath.Join(modules[0], "renamed.go"), "\n// unstaged change\n")
	writeTestFile(t, filepath.Join(modules[0], "newpkg", "new.go"), "package newpkg\n")
	writeTestFile(t, filepath.Join(repo, "outside.go"), "package outside\n")
	writeTestFile(t, filepath.Join(repo, "notes.txt"), "not a Go file\n")

	selection, err := SelectChangedPackages(context.Background(), "git", "go", repo, base, modules)
	if err != nil {
		t.Fatalf("SelectChangedPackages() error = %v", err)
	}
	wantPackages := []string{"example.test/a", "example.test/a/newpkg", "example.test/b"}
	gotPackages := make([]string, 0, len(selection.Packages))
	for _, packageInfo := range selection.Packages {
		gotPackages = append(gotPackages, packageInfo.ImportPath)
	}
	if fmt.Sprint(gotPackages) != fmt.Sprint(wantPackages) {
		t.Fatalf("selected packages = %v, want %v", gotPackages, wantPackages)
	}
	for _, expected := range []string{
		"a.go", "renamed.go", "b.go", "gone/gone.go", "newpkg/new.go", "outside.go",
	} {
		found := false
		for _, path := range selection.GoFiles {
			if strings.HasSuffix(path, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("changed Go files = %v, missing %q", selection.GoFiles, expected)
		}
	}
	wantUnowned := []string{"a/gone/gone.go", "outside.go"}
	if fmt.Sprint(selection.UnownedGoFiles) != fmt.Sprint(wantUnowned) {
		t.Fatalf("unowned Go files = %v, want %v", selection.UnownedGoFiles, wantUnowned)
	}
}

func TestRunChangedCoverageTestsOnlySelectedNumericPackages(t *testing.T) {
	repo, modules, base := newChangedPackageRepository(t)
	appendTestFile(t, filepath.Join(modules[0], "a.go"), "\n// changed\n")
	manifestPath := writeRegistrationManifest(t, `{"packages":[{"package":"example.test/a","minimum":80.00},{"package":"example.test/b","minimum":99.00}]}`)
	logPath := filepath.Join(t.TempDir(), "go-arguments.log")
	fakeGo := filepath.Join(t.TempDir(), "go")
	fakeGoScript := fmt.Sprintf(`#!/bin/sh
set -eu
log=%q
if [ "$1" = "list" ]; then
  case "$PWD" in
    */a) package_path=example.test/a ;;
    */b) package_path=example.test/b ;;
    *) echo "unexpected list directory: $PWD" >&2; exit 91 ;;
  esac
  printf '{"ImportPath":"%%s","Dir":"%%s"}\n' "$package_path" "$PWD"
  exit 0
fi
if [ "$1" = "test" ]; then
  profile=
  previous=
  for argument in "$@"; do
    if [ "$previous" = "-coverprofile" ]; then profile="$argument"; fi
    previous="$argument"
  done
  test -n "$profile"
  printf '%%s\n' "$*" >> "$log"
  printf 'mode: set\nexample.test/a/a.go:1.1,1.2 10 1\n' > "$profile"
  exit 0
fi
echo "unexpected go invocation: $*" >&2
exit 92
`, logPath)
	writeExecutable(t, fakeGo, fakeGoScript)

	var stdout, stderr bytes.Buffer
	err := runChangedCoverage(manifestPath, "git", fakeGo, repo, base, "30s", modules, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runChangedCoverage() error = %v (stdout %q, stderr %q)", err, stdout.String(), stderr.String())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake go log: %v", err)
	}
	if got := string(log); !strings.HasSuffix(strings.TrimSpace(got), " .") || strings.Contains(got, "./...") || strings.Contains(got, "example.test/b") {
		t.Fatalf("changed coverage test arguments = %q, want only selected package a", got)
	}
	if !strings.Contains(stdout.String(), "changed coverage passed: 1 changed package(s) checked across 1 module profile(s)") {
		t.Fatalf("changed coverage output = %q", stdout.String())
	}
}

func TestCompareSelectedIgnoresUnchangedPackagesAndReportsFloorFailure(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/a","minimum":80.00},{"package":"example/b","minimum":90.00},{"package":"example/exception","exception":"test-only"}]}`)
	passing := map[string]Coverage{
		"example/a": {Covered: 8, Total: 10},
		"example/b": {Covered: 0, Total: 10},
	}
	if err := CompareSelected(manifest, []string{"example/a"}, passing); err != nil {
		t.Fatalf("CompareSelected() rejected passing selected package: %v", err)
	}
	if err := CompareSelected(manifest, []string{"example/exception"}, nil); err != nil {
		t.Fatalf("CompareSelected() rejected manifest exception: %v", err)
	}

	err := CompareSelected(manifest, []string{"example/b"}, passing)
	if err == nil || !errors.Is(err, ErrCoverageFloorViolation) {
		t.Fatalf("CompareSelected() error = %v, want floor violation", err)
	}
	want := "coverage gate found coverage floor violations:\n- example/b: expected minimum 90.00%, actual 0.00%, delta -90.00%"
	if err.Error() != want {
		t.Fatalf("floor diagnostic = %q, want %q", err, want)
	}
}

func TestCompareSelectedReportsUnregisteredPackage(t *testing.T) {
	manifest := mustParseManifest(t, `{"packages":[{"package":"example/a","minimum":0.00}]}`)
	err := CompareSelected(manifest, []string{"example/missing"}, map[string]Coverage{})
	if err == nil || !errors.Is(err, ErrUnregisteredPackage) {
		t.Fatalf("CompareSelected() error = %v, want unregistered-package failure", err)
	}
	if !strings.Contains(err.Error(), "example/missing") {
		t.Fatalf("unregistered diagnostic = %q", err)
	}
}

func newChangedPackageRepository(t *testing.T) (string, []string, string) {
	t.Helper()
	repo := t.TempDir()
	moduleA := filepath.Join(repo, "a")
	moduleB := filepath.Join(repo, "b")
	writeTestFile(t, filepath.Join(moduleA, "go.mod"), "module example.test/a\n\ngo 1.26.7\n")
	writeTestFile(t, filepath.Join(moduleA, "a.go"), "package a\n")
	writeTestFile(t, filepath.Join(moduleA, "gone", "gone.go"), "package gone\n")
	writeTestFile(t, filepath.Join(moduleB, "go.mod"), "module example.test/b\n\ngo 1.26.7\n")
	writeTestFile(t, filepath.Join(moduleB, "b.go"), "package b\n")
	writeTestFile(t, filepath.Join(moduleB, "keep.go"), "package b\n")
	runTestGit(t, repo, "init", "-q")
	runTestGit(t, repo, "config", "user.email", "coveragegate-test@example.test")
	runTestGit(t, repo, "config", "user.name", "coveragegate test")
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-qm", "baseline")
	base := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	return repo, []string{moduleA, moduleB}, base
}

func runTestGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func appendTestFile(t *testing.T, path, contents string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s for append: %v", path, err)
	}
	if _, err := file.WriteString(contents); err != nil {
		_ = file.Close()
		t.Fatalf("append %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s after append: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
