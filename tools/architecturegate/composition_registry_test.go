package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalServicesPathIsNotTreatedAsLocalPeer(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	file := sourceFixture(t, "external.go", `package other
import "example.com/vendor/services/third/internal/impl"
`, false)
	pkg := &Package{ImportPath: "example.com/app/other", Dir: "/repo/other", Module: module, Files: []*SourceFile{file}}
	issues := architectureIssues(pkg, module, classifyService(pkg, module, policy), policy)
	if hasRule(issues, "peer-private-import") || hasRule(issues, "wire-import") {
		t.Fatalf("external service-shaped dependency was treated as local: %#v", issues)
	}
}

func TestCompositionRegistryAllowsOnlyExactHostTestSource(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	policy.CompositionRegistry = []CompositionEntry{{
		Module: module.Path, Package: module.Path + "/services/host",
		File: "services/host/host_test.go", Reason: "host assembles the public service wire",
	}}
	file := sourceFixtureAt(t, "/repo/services/host/host_test.go", `package host
import "example.com/app/services/peer/wire"
var _ = wire.NewService
`, true)
	file.RelPath = "services/host/host_test.go"
	pkg := &Package{ImportPath: module.Path + "/services/host", Dir: "/repo/services/host", Module: module, Files: []*SourceFile{file}}
	service := classifyService(pkg, module, policy)
	if service.Role != roleRoot {
		t.Fatalf("file-scoped composition entry elevated package: %#v", service)
	}
	issues := importIssues(pkg, module, service, file, "example.com/app/services/peer/wire", policy)
	if hasRule(issues, "wire-import") {
		t.Fatalf("registered host test could not assemble peer wire: %#v", issues)
	}

	file.RelPath = "services/host/other_test.go"
	issues = importIssues(pkg, module, service, file, "example.com/app/services/peer/wire", policy)
	if !hasRule(issues, "wire-import") {
		t.Fatalf("unregistered test source was allowed to import peer wire: %#v", issues)
	}
}

func TestCompositionRegistryMarksExternalConsumerPackage(t *testing.T) {
	module := &Module{Dir: "/repo/tests/embedding", Path: "example.com/agent-runtime-consumer"}
	pkg := &Package{ImportPath: module.Path, Dir: module.Dir, Module: module}
	policy := fixturePolicy()
	policy.CompositionRegistry = []CompositionEntry{{
		Module: module.Path, Package: module.Path,
		Reason: "external consumer owns application composition",
	}}
	if got := classifyService(pkg, module, policy); got.Role != roleComposition {
		t.Fatalf("external consumer was not classified as composition: %#v", got)
	}
}

func TestCompositionRegistryRejectsWildcardsAndNonTestFiles(t *testing.T) {
	for _, entry := range []CompositionEntry{
		{Module: "example.com/app/*", Package: "example.com/app/host", Reason: "wildcard module"},
		{Module: "example.com/app", Package: "example.com/app/*", Reason: "wildcard package"},
		{Module: "example.com/app", Package: "example.com/app/host", File: "host.go", Reason: "production file"},
		{Module: "example.com/app", Package: "example.com/app/host", File: "../host_test.go", Reason: "path traversal"},
		{Module: "example.com/app", Package: "example.com/app/host", File: "/host_test.go", Reason: "absolute path"},
	} {
		policy := fixturePolicy()
		policy.CompositionRegistry = []CompositionEntry{entry}
		if err := validatePolicy(policy); err == nil {
			t.Fatalf("composition entry %#v was accepted", entry)
		}
	}
}

func TestBaselineHistoryRequiresReviewedBaseline(t *testing.T) {
	err := applyBaseline(&Result{}, nil, runOptions{baselineBase: "origin/main", checkSet: map[string]bool{"architecture": true}}, Policy{}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "requires a reviewed") {
		t.Fatalf("applyBaseline error = %v; expected missing-baseline failure", err)
	}
}

func TestBaselineHistoryRejectsGrowthThroughRename(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	old := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app/services/a", File: "old.go", Symbol: "Run", Value: 81, Rationale: "extraction holder", Phase: "P0"}
	oldKey := baselineIssue(old).Key()
	initial := Baseline{Version: baselineVersion, Entries: []BaselineEntry{old}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "baseline")

	newEntry := old
	newEntry.File = "new.go"
	newEntry.Value = old.Value + 1
	current := Baseline{Version: baselineVersion, Entries: []BaselineEntry{newEntry}, Renames: []BaselineRename{{From: oldKey, To: baselineIssue(newEntry).Key()}}}
	issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
	if !hasRule(issues, "baseline-history-increase") {
		t.Fatalf("issues = %#v; renamed ceiling growth was accepted", issues)
	}
}

func TestBaselineHistoryRejectsMessageChange(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	old := BaselineEntry{Rule: "mutable-global", Module: "example.com/app", Package: "example.com/app/services/a", File: "state.go", Symbol: "Registry", Message: "old message", Rationale: "legacy holder", Phase: "P0"}
	initial := Baseline{Version: baselineVersion, Entries: []BaselineEntry{old}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "baseline")

	changed := old
	changed.Message = "new message"
	current := Baseline{Version: baselineVersion, Entries: []BaselineEntry{changed}}
	issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
	if !hasRule(issues, "baseline-history-increase") {
		t.Fatalf("issues = %#v; baseline message change was accepted", issues)
	}
}

func TestBaselineHistoryAcceptsInheritedBaselineAfterMainAdvances(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	writeFixture(t, root, "source.txt", "initial source\n")
	gitTestCommand(t, root, "add", "source.txt")
	gitTestCommand(t, root, "commit", "-m", "source")
	sourceCommitData, err := gitOutput(context.Background(), "git", root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sourceCommit := strings.TrimSpace(string(sourceCommitData))

	baseline := Baseline{Version: baselineVersion, SourceCommit: sourceCommit, Entries: []BaselineEntry{{
		Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 81,
		Rationale: "legacy holder", Phase: "P0",
	}}}
	data, err := baselineJSON(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "introduce reviewed baseline")
	gitTestCommand(t, root, "branch", "mainline")
	gitTestCommand(t, root, "checkout", "-b", "candidate")
	writeFixture(t, root, "candidate.txt", "candidate work\n")
	gitTestCommand(t, root, "add", "candidate.txt")
	gitTestCommand(t, root, "commit", "-m", "candidate work")
	gitTestCommand(t, root, "checkout", "mainline")
	writeFixture(t, root, "mainline.txt", "mainline work\n")
	gitTestCommand(t, root, "add", "mainline.txt")
	gitTestCommand(t, root, "commit", "-m", "advance mainline")
	gitTestCommand(t, root, "checkout", "candidate")

	mergeBaseData, err := gitOutput(context.Background(), "git", root, "merge-base", "mainline", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if mergeBase := strings.TrimSpace(string(mergeBaseData)); mergeBase == sourceCommit {
		t.Fatalf("fixture merge base = source commit %s; baseline introduction was not established first", sourceCommit)
	}
	issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "mainline", baseline)
	if len(issues) != 0 {
		t.Fatalf("inherited baseline after mainline advance = %#v", issues)
	}
}

func TestBaselineHistoryRejectsSourceReplacementOrRemoval(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	initial := Baseline{Version: baselineVersion, SourceCommit: "reviewed-source", Entries: []BaselineEntry{{
		Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 81,
		Rationale: "legacy holder", Phase: "P0",
	}}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "baseline")

	for name, sourceCommit := range map[string]string{
		"replacement": "replacement-source",
		"removal":     "",
	} {
		t.Run(name, func(t *testing.T) {
			current := initial
			current.SourceCommit = sourceCommit
			issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
			if !hasRule(issues, "baseline-history-source") {
				t.Fatalf("issues = %#v; source provenance change was accepted", issues)
			}
		})
	}
}

func TestBaselineHistoryPreservesDeletionAndReduction(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	initial := Baseline{Version: baselineVersion, SourceCommit: "reviewed-source", Entries: []BaselineEntry{{
		Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 81,
		Rationale: "legacy holder", Phase: "P0",
	}}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "baseline")

	reduced := initial
	reduced.Entries = []BaselineEntry{{
		Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 80,
		Rationale: "legacy holder", Phase: "P0",
	}}
	if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", reduced); len(issues) != 0 {
		t.Fatalf("reduced ceiling = %#v", issues)
	}

	deleted := initial
	deleted.Entries = nil
	if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", deleted); len(issues) != 0 {
		t.Fatalf("deleted exemption = %#v", issues)
	}
}

func TestBaselineHistoryRejectsAddedExemptionAndMissingRenameTarget(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	old := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 81, Rationale: "legacy holder", Phase: "P0"}
	initial := Baseline{Version: baselineVersion, SourceCommit: "reviewed-source", Entries: []BaselineEntry{old}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "baseline")

	added := initial
	added.Entries = append([]BaselineEntry(nil), initial.Entries...)
	added.Entries = append(added.Entries, BaselineEntry{Rule: "function-lines", Module: old.Module, Package: old.Package, File: "new.go", Symbol: "New", Value: 81, Rationale: "unreviewed holder", Phase: "P0"})
	if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", added); !hasRule(issues, "baseline-history-add") {
		t.Fatalf("issues = %#v; added exemption was accepted", issues)
	}

	invalidRename := initial
	invalidRename.Entries = nil
	invalidRename.Renames = []BaselineRename{{From: baselineIssue(old).Key(), To: baselineIssue(old).Key() + "-renamed"}}
	if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", invalidRename); !hasRule(issues, "baseline-history-rename") {
		t.Fatalf("issues = %#v; missing rename target was accepted", issues)
	}
}

func TestBaselineHistoryRejectsGrowthOfPersistedRename(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	old := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app/services/a", File: "old.go", Symbol: "Run", Value: 81, Rationale: "extraction holder", Phase: "P0"}
	target := old
	target.File = "new.go"
	rename := BaselineRename{From: baselineIssue(old).Key(), To: baselineIssue(target).Key()}
	initial := Baseline{Version: baselineVersion, SourceCommit: "reviewed-source", Entries: []BaselineEntry{target}, Renames: []BaselineRename{rename}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "persist renamed baseline")

	grown := target
	grown.Value++
	current := initial
	current.Entries = []BaselineEntry{grown}
	issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
	if hasRule(issues, "baseline-history-rename") {
		t.Fatalf("persisted rename was treated as invalid: %#v", issues)
	}
	if !hasRule(issues, "baseline-history-increase") {
		t.Fatalf("issues = %#v; persisted renamed ceiling growth was accepted", issues)
	}
}

func TestBaselineHistoryRejectsRetainedOrUnknownRenameSource(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	old := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 81, Rationale: "legacy holder", Phase: "P0"}
	initial := Baseline{Version: baselineVersion, SourceCommit: "reviewed-source", Entries: []BaselineEntry{old}}
	data, err := baselineJSON(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "baseline")

	t.Run("retained source", func(t *testing.T) {
		target := old
		target.File = "new.go"
		current := initial
		current.Entries = []BaselineEntry{old, target}
		current.Renames = []BaselineRename{{From: baselineIssue(old).Key(), To: baselineIssue(target).Key()}}
		issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
		if !hasRule(issues, "baseline-history-rename") {
			t.Fatalf("issues = %#v; retained rename source was accepted", issues)
		}
	})

	t.Run("unknown source", func(t *testing.T) {
		unknown := old
		unknown.File = "unknown.go"
		current := initial
		current.Renames = []BaselineRename{{From: baselineIssue(unknown).Key(), To: baselineIssue(old).Key()}}
		issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
		if !hasRule(issues, "baseline-history-rename") {
			t.Fatalf("issues = %#v; unknown rename source was accepted", issues)
		}
	})
}

func TestBaselineHistoryRejectsUnsupportedHistoricalVersion(t *testing.T) {
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline.json")
	entry := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app", File: "legacy.go", Symbol: "Run", Value: 81, Rationale: "legacy holder", Phase: "P0"}
	historical := Baseline{Version: 999, SourceCommit: "reviewed-source", Entries: []BaselineEntry{entry}}
	data, err := baselineJSON(historical)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "baseline.json")
	gitTestCommand(t, root, "commit", "-m", "unsupported historical baseline")

	current := historical
	current.Version = baselineVersion
	issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current)
	for _, issue := range issues {
		if issue.Rule == "baseline-history" && strings.Contains(issue.Message, "version 999") {
			return
		}
	}
	t.Fatalf("issues = %#v; unsupported historical version was accepted", issues)
}

func TestBaselineHistoryBootstrapsFromMergeBaseSource(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "mod/go.mod", "module example.com/bootstrap\n\ngo 1.26.7\n")
	writeFixture(t, root, "mod/large.go", `package bootstrap
func Large() {
  _ = 1
  _ = 2
  _ = 3
}
`)
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "mod/go.mod", "mod/large.go")
	gitTestCommand(t, root, "commit", "-m", "source")
	sourceCommitData, err := gitOutput(context.Background(), "git", root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sourceCommit := strings.TrimSpace(string(sourceCommitData))

	policy := fixturePolicy()
	policy.ModuleDirs = []string{"mod"}
	policy.Limits.FunctionStatements = 1
	policy.Baseline = "baseline.json"
	module := &Module{Path: "example.com/bootstrap", Dir: filepath.Join(root, "mod")}
	oldSource := sourceFixtureAt(t, filepath.Join(module.Dir, "large.go"), "package bootstrap\nfunc Large() {\n  _ = 1\n  _ = 2\n  _ = 3\n}\n", false)
	oldIssues := sizeIssues(&Package{ImportPath: module.Path, Dir: module.Dir, Files: []*SourceFile{oldSource}}, module, policy)
	if len(oldIssues) == 0 {
		t.Fatal("fixture did not produce a historical size issue")
	}
	entries := make([]BaselineEntry, len(oldIssues))
	for index, issue := range oldIssues {
		entries[index] = BaselineEntry{Rule: issue.Rule, Module: issue.Module, Package: issue.Package, File: issue.File, Symbol: issue.Symbol, Value: issue.Value, Message: issue.Message, Rationale: "initial inventory", Phase: "P0"}
	}
	baseline := Baseline{Version: baselineVersion, SourceCommit: sourceCommit, Entries: entries}
	baselineData, err := baselineJSON(baseline)
	if err != nil {
		t.Fatal(err)
	}
	baselinePath := filepath.Join(root, "baseline.json")
	if err := os.WriteFile(baselinePath, baselineData, 0o600); err != nil {
		t.Fatal(err)
	}
	issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", baseline, policy)
	if len(issues) != 0 {
		t.Fatalf("bootstrap issues = %#v", issues)
	}

	baseline.Entries = append(baseline.Entries, BaselineEntry{Rule: "function-lines", Module: module.Path, Package: module.Path, File: "new.go", Symbol: "New", Value: 81, Rationale: "unrelated", Phase: "P0"})
	issues = compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", baseline, policy)
	if !hasRule(issues, "baseline-history-add") {
		t.Fatalf("bootstrap accepted an issue absent at merge base: %#v", issues)
	}

	oldEntry := entries[0]
	targetEntry := oldEntry
	targetEntry.File = "renamed.go"
	rename := BaselineRename{From: baselineIssue(oldEntry).Key(), To: baselineIssue(targetEntry).Key()}
	for name, current := range map[string]Baseline{
		"retained source": {Version: baselineVersion, SourceCommit: sourceCommit, Entries: []BaselineEntry{oldEntry, targetEntry}, Renames: []BaselineRename{rename}},
		"missing target":  {Version: baselineVersion, SourceCommit: sourceCommit, Entries: []BaselineEntry{oldEntry}, Renames: []BaselineRename{rename}},
	} {
		t.Run("invalid rename/"+name, func(t *testing.T) {
			if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", current, policy); !hasRule(issues, "baseline-history-rename") {
				t.Fatalf("issues = %#v; invalid bootstrap rename was accepted", issues)
			}
		})
	}
	valid := baseline
	valid.Entries = append([]BaselineEntry{targetEntry}, entries[1:]...)
	valid.Renames = []BaselineRename{rename}
	if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", valid, policy); len(issues) != 0 {
		t.Fatalf("valid bootstrap rename = %#v", issues)
	}
}

func TestBaselineHistoryRejectsInvalidBootstrapProvenance(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "source.txt", "source\n")
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "source.txt")
	gitTestCommand(t, root, "commit", "-m", "source")

	for name, sourceCommit := range map[string]string{
		"missing": "",
		"wrong":   "not-the-merge-base",
	} {
		t.Run(name, func(t *testing.T) {
			current := Baseline{Version: baselineVersion, SourceCommit: sourceCommit}
			issues := compareBaselineHistory(context.Background(), "git", root, filepath.Join(root, "baseline.json"), "HEAD", current)
			if !hasRule(issues, "baseline-history-source") {
				t.Fatalf("issues = %#v; invalid bootstrap provenance was accepted", issues)
			}
		})
	}
}

func TestBaselineBootstrapUsesHistoricalTypesForPublicLeaks(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "mod/go.mod", "module example.com/bootstrap\n\ngo 1.26.7\n")
	writeFixture(t, root, "mod/services/thing/service.go", `package thing

import "example.com/bootstrap/services/thing/internal"

type Service interface {
	State() internal.State
}
`)

	writeFixture(t, root, "mod/services/thing/internal/state.go", `package internal

type State struct{}
`)
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	gitTestCommand(t, root, "add", "mod/go.mod", "mod/services/thing/service.go", "mod/services/thing/internal/state.go")
	gitTestCommand(t, root, "commit", "-m", "source")
	sourceCommitData, err := gitOutput(context.Background(), "git", root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sourceCommit := strings.TrimSpace(string(sourceCommitData))

	policy := fixturePolicy()
	policy.ModuleDirs = []string{"mod"}
	old, err := measureBootstrapSource(context.Background(), "git", root, "HEAD", policy)
	if err != nil {
		t.Fatal(err)
	}
	var leak Issue
	for _, issue := range old.Issues {
		if issue.Rule == "public-implementation-leak" {
			leak = issue
			break
		}
	}
	if leak.Rule == "" {
		t.Fatalf("historical type load did not find public leak: %#v", old.Issues)
	}

	baseline := Baseline{Version: baselineVersion, SourceCommit: sourceCommit, Entries: []BaselineEntry{{
		Rule: leak.Rule, Module: leak.Module, Package: leak.Package, File: leak.File,
		Symbol: leak.Symbol, Value: leak.Value, Message: leak.Message,
		Rationale: "historical public API leak", Phase: "P0",
	}}}
	data, err := baselineJSON(baseline)
	if err != nil {
		t.Fatal(err)
	}
	baselinePath := filepath.Join(root, "baseline.json")
	if err := os.WriteFile(baselinePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if issues := compareBaselineHistory(context.Background(), "git", root, baselinePath, "HEAD", baseline, policy); len(issues) != 0 {
		t.Fatalf("historical public leak was not accepted: %#v", issues)
	}
}

func TestInventoryIncludesUntrackedSourceViolations(t *testing.T) {
	// The size/architecture inventory reads the selected module source through
	// go list; it must inspect a newly created file even when no Git index entry
	// exists yet. This closes the gap where a changed-from-revision linter can
	// miss an untracked file before the first commit.
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/untracked\n\ngo 1.26.7\n")
	writeFixture(t, root, "new.go", `package untracked

func NewViolation() {
	_ = 1
	_ = 2
	_ = 3
}
`)
	modules, err := discoverModules(context.Background(), "go", root, []string{"."}, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	policy := fixturePolicy()
	policy.Limits.FunctionLines = 2
	result, err := evaluate(context.Background(), modules, policy, map[string]bool{"size": true}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(result.Issues, "function-lines") {
		t.Fatalf("untracked source was not measured: %#v", result.Issues)
	}
}
