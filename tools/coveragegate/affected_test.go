package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newAffectedRepository builds a two-module workspace:
//
//	a/leaf <- a/mid <- a/top (tested) ; a/mid tested ; a/other (tested, unrelated)
//	b/user (tested) imports a/mid ; embed/consumer (standalone, tested) imports a/leaf
func newAffectedRepository(t *testing.T) (string, []AffectedModule, string) {
	t.Helper()
	repo := t.TempDir()
	writeTestFile(t, filepath.Join(repo, "go.work"), "go 1.26.7\n\nuse (\n\t./a\n\t./b\n)\n")
	writeTestFile(t, filepath.Join(repo, "a", "go.mod"), "module example.test/a\n\ngo 1.26.7\n")
	writeTestFile(t, filepath.Join(repo, "a", "leaf", "leaf.go"), "package leaf\n\nfunc Leaf() int { return 1 }\n")
	writeTestFile(t, filepath.Join(repo, "a", "leaf", "testdata", "input.txt"), "fixture\n")
	writeTestFile(t, filepath.Join(repo, "a", "mid", "mid.go"), "package mid\n\nimport \"example.test/a/leaf\"\n\nfunc Mid() int { return leaf.Leaf() }\n")
	writeTestFile(t, filepath.Join(repo, "a", "mid", "mid_test.go"), "package mid\n\nimport \"testing\"\n\nfunc TestMid(t *testing.T) { _ = Mid() }\n")
	writeTestFile(t, filepath.Join(repo, "a", "top", "top.go"), "package top\n\nimport \"example.test/a/mid\"\n\nfunc Top() int { return mid.Mid() }\n")
	writeTestFile(t, filepath.Join(repo, "a", "top", "top_test.go"), "package top_test\n\nimport (\n\t\"testing\"\n\n\t\"example.test/a/top\"\n)\n\nfunc TestTop(t *testing.T) { _ = top.Top() }\n")
	writeTestFile(t, filepath.Join(repo, "a", "other", "other.go"), "package other\n\nfunc Other() int { return 2 }\n")
	writeTestFile(t, filepath.Join(repo, "a", "other", "other_test.go"), "package other\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) { _ = Other() }\n")
	writeTestFile(t, filepath.Join(repo, "a", "README.md"), "docs\n")
	writeTestFile(t, filepath.Join(repo, "a", "notes.txt"), "module-level data\n")
	writeTestFile(t, filepath.Join(repo, "b", "go.mod"), "module example.test/b\n\ngo 1.26.7\n\nrequire example.test/a v0.0.0\n")
	writeTestFile(t, filepath.Join(repo, "b", "user", "user.go"), "package user\n\nfunc User() int { return 3 }\n")
	writeTestFile(t, filepath.Join(repo, "b", "user", "user_test.go"), "package user\n\nimport (\n\t\"testing\"\n\n\t\"example.test/a/mid\"\n)\n\nfunc TestUser(t *testing.T) { _ = mid.Mid() + User() }\n")
	writeTestFile(t, filepath.Join(repo, "embed", "go.mod"), "module example.test/embed\n\ngo 1.26.7\n\nrequire example.test/a v0.0.0\n\nreplace example.test/a => ../a\n")
	writeTestFile(t, filepath.Join(repo, "embed", "consumer", "consumer_test.go"), "package consumer\n\nimport (\n\t\"testing\"\n\n\t\"example.test/a/leaf\"\n)\n\nfunc TestConsumer(t *testing.T) { _ = leaf.Leaf() }\n")
	writeTestFile(t, filepath.Join(repo, "coverage-manifest", "a", "other.json"), "{}\n")
	runTestGit(t, repo, "init", "-q")
	runTestGit(t, repo, "config", "user.email", "coveragegate-test@example.test")
	runTestGit(t, repo, "config", "user.name", "coveragegate test")
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-qm", "baseline")
	base := strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
	modules := []AffectedModule{
		{Directory: filepath.Join(repo, "a")},
		{Directory: filepath.Join(repo, "b")},
		{Directory: filepath.Join(repo, "embed"), Standalone: true},
	}
	return repo, modules, base
}

func selectAffectedForTest(t *testing.T, repo string, modules []AffectedModule, base string) string {
	t.Helper()
	scope, err := SelectAffectedScope(context.Background(), "git", "go", repo, base, "", modules)
	if err != nil {
		t.Fatalf("SelectAffectedScope() error = %v", err)
	}
	var output bytes.Buffer
	if err := WriteAffectedScope(&output, scope); err != nil {
		t.Fatalf("WriteAffectedScope() error = %v", err)
	}
	return output.String()
}

func TestSelectAffectedScopeSelectsReverseClosureAcrossModules(t *testing.T) {
	repo, modules, base := newAffectedRepository(t)
	appendTestFile(t, filepath.Join(repo, "a", "leaf", "leaf.go"), "\n// changed\n")

	got := selectAffectedForTest(t, repo, modules, base)
	want := strings.Join([]string{
		"scope changed",
		"changed example.test/a/leaf",
		"test a ./mid",
		"test a ./top",
		"test b ./user",
		"test embed ./consumer",
		"check example.test/a/leaf",
		"check example.test/a/mid",
		"check example.test/a/top",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("scope =\n%s\nwant\n%s", got, want)
	}
}

func TestSelectAffectedScopeMapsTestdataAndManifestToOwningPackage(t *testing.T) {
	repo, modules, base := newAffectedRepository(t)
	appendTestFile(t, filepath.Join(repo, "a", "leaf", "testdata", "input.txt"), "more\n")
	appendTestFile(t, filepath.Join(repo, "coverage-manifest", "a", "other.json"), "\n")

	got := selectAffectedForTest(t, repo, modules, base)
	for _, line := range []string{"changed example.test/a/leaf", "changed example.test/a/other", "test a ./other", "check example.test/a/other"} {
		if !strings.Contains(got, line+"\n") {
			t.Fatalf("scope missing %q:\n%s", line, got)
		}
	}
	if strings.Contains(got, "check example.test/b/user") {
		t.Fatalf("scope checks unaffected package:\n%s", got)
	}
}

func TestSelectAffectedScopeNoChangesSelectsNothing(t *testing.T) {
	repo, modules, base := newAffectedRepository(t)
	writeTestFile(t, filepath.Join(repo, "a", "CHANGES.md"), "docs only\n")
	writeTestFile(t, filepath.Join(repo, "docs.txt"), "outside every module\n")

	if got := selectAffectedForTest(t, repo, modules, base); got != "scope changed\n" {
		t.Fatalf("scope = %q, want no tests or checks", got)
	}
}

func TestSelectAffectedScopeFallsBackToFullForUnscopableChanges(t *testing.T) {
	cases := map[string]string{
		"b/go.mod":                "changed b/go.mod",
		"go.work":                 "changed go.work",
		"Makefile":                "changed Makefile",
		"tools/coveragegate/x.go": "changed tools/coveragegate/x.go",
		"a/notes.txt":             "changed a/notes.txt outside every package",
	}
	for path, reason := range cases {
		t.Run(path, func(t *testing.T) {
			repo, modules, base := newAffectedRepository(t)
			target := filepath.Join(repo, filepath.FromSlash(path))
			if _, err := os.Stat(target); err == nil {
				appendTestFile(t, target, "\n")
			} else {
				writeTestFile(t, target, "new\n")
			}
			got := selectAffectedForTest(t, repo, modules, base)
			if got != "scope full "+reason+"\n" {
				t.Fatalf("scope = %q, want full scope for %s", got, reason)
			}
		})
	}
}

func TestGateSelectedChecksOnlySelectedFloors(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest.json")
	writeTestFile(t, manifestPath, `{"packages": [
		{"package": "example.test/a/checked", "minimum": 50.00},
		{"package": "example.test/a/unmeasured", "minimum": 80.00},
		{"package": "example.test/a/zero", "minimum": 0.00}
	]}`)
	profilePath := filepath.Join(directory, "a.out")
	writeTestFile(t, profilePath, "mode: set\nexample.test/a/checked/c.go:1.1,2.1 3 1\nexample.test/a/checked/c.go:3.1,4.1 1 0\n")
	selectPath := filepath.Join(directory, "select.txt")

	writeTestFile(t, selectPath, "example.test/a/checked\nexample.test/a/zero\n")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--manifest", manifestPath, "--select", selectPath, profilePath}, &stdout, &stderr); err != nil {
		t.Fatalf("run(--select) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "1 selected packages checked across 1 profiles") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	writeTestFile(t, selectPath, "example.test/a/checked\nexample.test/a/unmeasured\n")
	err := run([]string{"--manifest", manifestPath, "--select", selectPath, profilePath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "example.test/a/unmeasured") {
		t.Fatalf("run(--select) error = %v, want unmeasured selected package", err)
	}
}

// commitReader adds a test package a/reader whose test names expression,
// commits it, and returns the new base.
func commitReader(t *testing.T, repo, body string) string {
	t.Helper()
	writeTestFile(t, filepath.Join(repo, "a", "reader", "reader_test.go"),
		"package reader\n\nimport (\n\t\"path/filepath\"\n\t\"testing\"\n)\n\nfunc TestRead(t *testing.T) {\n\troot := t.TempDir()\n\t_ = root\n\t_ = filepath.Join\n\t"+body+"\n}\n")
	runTestGit(t, repo, "add", "-A")
	runTestGit(t, repo, "commit", "-qm", "add reader")
	return strings.TrimSpace(runTestGit(t, repo, "rev-parse", "HEAD"))
}

func TestSelectAffectedScopeFallsBackToFullWhenAnotherPackageReadsChangedData(t *testing.T) {
	readers := map[string]string{
		"relative join":     `_ = filepath.Join("..", "leaf", "testdata")`,
		"relative constant": `_ = "../leaf/testdata/input.txt"`,
		"repository path":   `_ = "a/leaf/testdata"`,
		"joined with base":  `_ = filepath.Join(root, "leaf", "testdata", "input.txt")`,
	}
	for name, body := range readers {
		t.Run(name, func(t *testing.T) {
			repo, modules, _ := newAffectedRepository(t)
			base := commitReader(t, repo, body)
			appendTestFile(t, filepath.Join(repo, "a", "leaf", "testdata", "input.txt"), "more\n")

			got := selectAffectedForTest(t, repo, modules, base)
			want := "scope full changed a/leaf/testdata/input.txt is referenced from a/reader/reader_test.go\n"
			if got != want {
				t.Fatalf("scope = %q, want %q", got, want)
			}
		})
	}
}

func TestSelectAffectedScopeIgnoresOwnAndUnrelatedPathLiterals(t *testing.T) {
	repo, modules, _ := newAffectedRepository(t)
	writeTestFile(t, filepath.Join(repo, "a", "leaf", "leaf_test.go"),
		"package leaf\n\nimport \"testing\"\n\nfunc TestLeaf(t *testing.T) { _ = \"testdata/input.txt\" }\n")
	base := commitReader(t, repo, `_ = []string{".", "../..", "phase/..", "a", "leaf", "testdata", "%s/leaf/testdata"}`)
	appendTestFile(t, filepath.Join(repo, "a", "leaf", "testdata", "input.txt"), "more\n")

	got := selectAffectedForTest(t, repo, modules, base)
	if !strings.HasPrefix(got, "scope changed\nchanged example.test/a/leaf\n") {
		t.Fatalf("scope = %q, want the changed scope for a/leaf", got)
	}
}
