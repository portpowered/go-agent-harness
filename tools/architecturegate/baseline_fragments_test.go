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
