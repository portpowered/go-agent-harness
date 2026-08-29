package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveAcceptsPinnedVersionsForBothAnalyzers(t *testing.T) {
	for _, fixture := range analyzerFixtures() {
		t.Run(fixture.tool, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), fixture.binaryName)
			writeExecutable(t, candidate, fixture.versionScript(fixture.expectedVersion))
			cfg := fixture.config(candidate, filepath.Join(t.TempDir(), "tools"), "")

			resolved, err := resolve(context.Background(), cfg, nil)
			if err != nil {
				t.Fatalf("resolve() error = %v", err)
			}
			want, err := absolutePath(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if resolved != want {
				t.Fatalf("resolved path = %q, want %q", resolved, want)
			}
		})
	}
}

func TestResolveInstallsPinnedVersionWhenCandidateIsMissingForBothAnalyzers(t *testing.T) {
	for _, fixture := range analyzerFixtures() {
		t.Run(fixture.tool, func(t *testing.T) {
			root := t.TempDir()
			installedSource := filepath.Join(root, "installed-source")
			writeExecutable(t, installedSource, fixture.versionScript(fixture.expectedVersion))
			fakeGo := writeFakeInstaller(t, root, fixture.binaryName, installedSource, nil)
			toolDir := filepath.Join(root, "cache")
			cfg := fixture.config(filepath.Join(root, "missing", fixture.binaryName), toolDir, fakeGo)

			resolved, err := resolve(context.Background(), cfg, nil)
			if err != nil {
				t.Fatalf("resolve() error = %v", err)
			}
			if resolved != installedPath(cfg) {
				t.Fatalf("resolved path = %q, want deterministic installed path %q", resolved, installedPath(cfg))
			}
			if !executableFile(resolved) {
				t.Fatalf("resolved installed path %q is not executable", resolved)
			}
			installLog := mustReadFile(t, filepath.Join(root, "install.log"))
			if !strings.Contains(installLog, "install\n") || !strings.Contains(installLog, fixture.installPackage+"@"+fixture.expectedVersion) {
				t.Fatalf("install log = %q, want pinned package", installLog)
			}
		})
	}
}

func TestResolveReplacesMismatchedCandidateForBothAnalyzers(t *testing.T) {
	for _, fixture := range analyzerFixtures() {
		t.Run(fixture.tool, func(t *testing.T) {
			root := t.TempDir()
			candidate := filepath.Join(root, fixture.binaryName)
			writeExecutable(t, candidate, fixture.versionScript(fixture.wrongVersion))
			installedSource := filepath.Join(root, "installed-source")
			writeExecutable(t, installedSource, fixture.versionScript(fixture.expectedVersion))
			fakeGo := writeFakeInstaller(t, root, fixture.binaryName, installedSource, nil)
			cfg := fixture.config(candidate, filepath.Join(root, "cache"), fakeGo)

			resolved, err := resolve(context.Background(), cfg, nil)
			if err != nil {
				t.Fatalf("resolve() error = %v", err)
			}
			if resolved != installedPath(cfg) {
				t.Fatalf("resolved path = %q, want installed path %q", resolved, installedPath(cfg))
			}
			if strings.Contains(resolved, candidate) {
				t.Fatalf("resolver returned mismatched candidate %q", resolved)
			}
		})
	}
}

func TestResolveReportsFailedInstallForBothAnalyzers(t *testing.T) {
	for _, fixture := range analyzerFixtures() {
		t.Run(fixture.tool, func(t *testing.T) {
			root := t.TempDir()
			fakeGo := writeFakeInstaller(t, root, fixture.binaryName, "", errors.New("network unavailable"))
			cfg := fixture.config(filepath.Join(root, "missing", fixture.binaryName), filepath.Join(root, "cache"), fakeGo)

			_, err := resolve(context.Background(), cfg, nil)
			if err == nil {
				t.Fatal("resolve() succeeded despite failed install")
			}
			for _, expected := range []string{
				fixture.tool,
				"expected version " + fixture.normalizedVersion(),
				"attempted remedy:",
				"recovery:",
				"go install failed",
			} {
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("resolve() error = %q, want substring %q", err, expected)
				}
			}
		})
	}
}

func TestResolveRejectsPostInstallVersionMismatchForBothAnalyzers(t *testing.T) {
	for _, fixture := range analyzerFixtures() {
		t.Run(fixture.tool, func(t *testing.T) {
			root := t.TempDir()
			installedSource := filepath.Join(root, "installed-source")
			writeExecutable(t, installedSource, fixture.versionScript(fixture.wrongVersion))
			fakeGo := writeFakeInstaller(t, root, fixture.binaryName, installedSource, nil)
			cfg := fixture.config(filepath.Join(root, "missing", fixture.binaryName), filepath.Join(root, "cache"), fakeGo)

			_, err := resolve(context.Background(), cfg, nil)
			if err == nil {
				t.Fatal("resolve() accepted a post-install version mismatch")
			}
			for _, expected := range []string{
				"expected version " + fixture.normalizedVersion(),
				"installed executable",
				fixture.wrongVersion,
				"recovery:",
			} {
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("resolve() error = %q, want substring %q", err, expected)
				}
			}
		})
	}
}

func TestReportedVersionNormalizesLeadingV(t *testing.T) {
	got, err := reportedVersion("golangci-lint has version v2.3.0 built with go1.26.7")
	if err != nil {
		t.Fatalf("reportedVersion() error = %v", err)
	}
	if got != "2.3.0" {
		t.Fatalf("reported version = %q, want 2.3.0", got)
	}
}

func TestRunPrintsOnlyResolvedPath(t *testing.T) {
	fixture := analyzerFixtures()[0]
	root := t.TempDir()
	candidate := filepath.Join(root, fixture.binaryName)
	writeExecutable(t, candidate, fixture.versionScript(fixture.expectedVersion))
	workingDirectory, err := absolutePath(root)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	err = run([]string{
		"--tool", fixture.tool,
		"--expected-version", fixture.expectedVersion,
		"--candidate", candidate,
		"--tool-dir", filepath.Join(root, "cache"),
		"--working-dir", workingDirectory,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v (stderr %q)", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != candidate {
		t.Fatalf("stdout = %q, want resolved path %q", stdout.String(), candidate)
	}
}

type analyzerFixture struct {
	tool            string
	binaryName      string
	installPackage  string
	expectedVersion string
	wrongVersion    string
	versionArgs     []string
}

func analyzerFixtures() []analyzerFixture {
	return []analyzerFixture{
		{
			tool:            "golangci-lint",
			binaryName:      "golangci-lint",
			installPackage:  "example.test/golangci-lint",
			expectedVersion: "v2.3.0",
			wrongVersion:    "2.3.1",
			versionArgs:     []string{"version"},
		},
		{
			tool:            "staticcheck",
			binaryName:      "staticcheck",
			installPackage:  "example.test/staticcheck",
			expectedVersion: "2025.1.1",
			wrongVersion:    "2025.1.2",
			versionArgs:     []string{"-version"},
		},
	}
}

func (f analyzerFixture) config(candidate, toolDirectory, goBinary string) config {
	if goBinary == "" {
		goBinary = "go"
	}
	cfg, err := newConfig(configInput{
		tool:             f.tool,
		expectedVersion:  f.expectedVersion,
		candidate:        candidate,
		goBinary:         goBinary,
		installPackage:   f.installPackage,
		toolDirectory:    toolDirectory,
		workingDirectory: ".",
		versionArgs:      f.versionArgs,
	})
	if err != nil {
		panic(fmt.Sprintf("fixture config: %v", err))
	}
	return cfg
}

func (f analyzerFixture) normalizedVersion() string {
	version, err := normalizeVersion(f.expectedVersion)
	if err != nil {
		panic(fmt.Sprintf("fixture version: %v", err))
	}
	return version
}

func (f analyzerFixture) versionScript(version string) string {
	normalized, err := normalizeVersion(version)
	if err != nil {
		panic(fmt.Sprintf("fixture version: %v", err))
	}
	if f.tool == "golangci-lint" {
		return fmt.Sprintf("#!/bin/sh\nprintf 'golangci-lint has version v%s built with go1.26.7\\n'\n", normalized)
	}
	return fmt.Sprintf("#!/bin/sh\nprintf 'staticcheck %s (0.6.1)\\n'\n", normalized)
}

func writeFakeInstaller(t *testing.T, root, binaryName, source string, installErr error) string {
	t.Helper()
	path := filepath.Join(root, "fake-go")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\n")
	script.WriteString("if [ \"${1:-}\" != install ]; then echo 'unexpected go command' >&2; exit 91; fi\n")
	script.WriteString("printf '%s\\n' \"$@\" > ")
	script.WriteString(shellQuote(filepath.Join(root, "install.log")))
	script.WriteString("\n")
	if installErr != nil {
		script.WriteString("echo ")
		script.WriteString(shellQuote(installErr.Error()))
		script.WriteString(" >&2\nexit 23\n")
	} else {
		script.WriteString("mkdir -p \"$GOBIN\"\n")
		script.WriteString("cp ")
		script.WriteString(shellQuote(source))
		script.WriteString(" \"$GOBIN/")
		script.WriteString(binaryName)
		if runtime.GOOS == "windows" {
			script.WriteString(".exe")
		}
		script.WriteString("\"\n")
		script.WriteString("chmod +x \"$GOBIN/")
		script.WriteString(binaryName)
		if runtime.GOOS == "windows" {
			script.WriteString(".exe")
		}
		script.WriteString("\"\n")
	}
	writeExecutable(t, path, script.String())
	return path
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create executable directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
