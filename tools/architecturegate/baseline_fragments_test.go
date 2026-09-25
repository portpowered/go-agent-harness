package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaselineDirectoryLoadsFragmentsAndRejectsDuplicates(t *testing.T) {
	root := t.TempDir()
	first := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app/a", File: "a/a.go", Symbol: "A", Value: 81, Rationale: "legacy", Phase: "P0"}
	second := BaselineEntry{Rule: "file-lines", Module: "example.com/app", Package: "example.com/app/b", File: "b/b.go", Value: 401, Rationale: "legacy", Phase: "P0"}
	for path, baseline := range map[string]Baseline{
		"baselines/a.json": {Version: baselineVersion, SourceCommit: "reviewed", Entries: []BaselineEntry{first}},
		"baselines/b.json": {Version: baselineVersion, SourceCommit: "reviewed", Entries: []BaselineEntry{second}},
	} {
		data, err := baselineJSON(baseline)
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, root, path, string(data))
	}
	loaded, err := loadBaseline("baselines", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 2 || loaded.SourceCommit != "reviewed" {
		t.Fatalf("loaded baseline = %#v", loaded)
	}
	duplicate, err := baselineJSON(Baseline{Version: baselineVersion, SourceCommit: "reviewed", Entries: []BaselineEntry{first}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "baselines/duplicate.json", string(duplicate))
	if _, err := loadBaseline("baselines", root); err == nil || !strings.Contains(err.Error(), "duplicate entry") {
		t.Fatalf("duplicate fragment error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "baselines", "duplicate.json")); err != nil {
		t.Fatal(err)
	}
	conflict, err := baselineJSON(Baseline{Version: baselineVersion, SourceCommit: "different", Entries: []BaselineEntry{first}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "baselines/conflict.json", string(conflict))
	if _, err := loadBaseline("baselines", root); err == nil || !strings.Contains(err.Error(), "source_commit") {
		t.Fatalf("conflicting fragment error = %v", err)
	}
}

func TestHistoricalBaselineDirectoryAggregatesFragments(t *testing.T) {
	root := t.TempDir()
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	entry := BaselineEntry{Rule: "file-lines", Module: "example.com/app", Package: "example.com/app", File: "app.go", Value: 401, Rationale: "legacy", Phase: "P0"}
	data, err := baselineJSON(Baseline{Version: baselineVersion, SourceCommit: "reviewed", Entries: []BaselineEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "baselines/app.json", string(data))
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "sharded baseline")
	baseline, found, err := loadHistoricalBaseline(context.Background(), "git", root, "HEAD", "baselines")
	if err != nil || !found || len(baseline.Entries) != 1 {
		t.Fatalf("historical baseline = %#v, found=%v, err=%v", baseline, found, err)
	}
}

func TestForbiddenImportExceptAllowsOnlyTheSharedContract(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/gateway"}
	policy := fixturePolicy()
	policy.ForbiddenImports = []ImportRule{{
		From:    []string{"example.com/gateway/test/functional"},
		Imports: []string{"example.com/loop", "example.com/loop/**"},
		Except:  []string{"example.com/loop/pkg/messages"},
		Reason:  "gateway consumers depend only on the shared loop message contract",
	}}
	file := sourceFixture(t, "consumer_test.go", `package functional
import "example.com/loop/pkg/engine"
var _ engine.Engine
`, true)
	pkg := &Package{ImportPath: module.Path + "/test/functional", Dir: "/repo/test/functional", Module: module, Files: []*SourceFile{file}}
	if issues := importIssues(pkg, module, serviceInfo{}, file, "example.com/loop/pkg/engine", policy); !hasRule(issues, "forbidden-import") {
		t.Fatalf("non-contract loop import was accepted: %#v", issues)
	}
	if issues := importIssues(pkg, module, serviceInfo{}, file, "example.com/loop/pkg/messages", policy); hasRule(issues, "forbidden-import") {
		t.Fatalf("excepted contract import was rejected: %#v", issues)
	}
	other := &Package{ImportPath: module.Path + "/pkg/providers", Dir: "/repo/pkg/providers", Module: module, Files: []*SourceFile{file}}
	if issues := importIssues(other, module, serviceInfo{}, file, "example.com/loop/pkg/engine", policy); hasRule(issues, "forbidden-import") {
		t.Fatalf("rule applied outside its from scope: %#v", issues)
	}
}
