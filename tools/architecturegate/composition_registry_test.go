package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopedBaselineKeepsOnlySelectedModules(t *testing.T) {
	first := &Module{Path: "example.com/first", Dir: "/repo/first"}
	second := &Module{Path: "example.com/second", Dir: "/repo/second"}
	baseline := Baseline{Version: baselineVersion, Entries: []BaselineEntry{
		{Rule: "file-lines", Module: first.Path, Package: first.Path, File: "first.go", Value: 401, Rationale: "legacy", Phase: "P0"},
		{Rule: "file-lines", Module: second.Path, Package: second.Path, File: "second.go", Value: 401, Rationale: "legacy", Phase: "P0"},
	}}
	filtered := baselineForModules(baseline, []*Module{first})
	if len(filtered.Entries) != 1 || filtered.Entries[0].Module != first.Path {
		t.Fatalf("filtered baseline = %#v", filtered.Entries)
	}
}

func TestScopedBaselineKeepsStaleEntriesInsideSelectedPrefix(t *testing.T) {
	module := &Module{
		Path: "example.com/app",
		Dir:  "/repo/app",
		Packages: []*Package{
			{ImportPath: "example.com/app/internal/acceptance"},
		},
	}
	selected := BaselineEntry{Rule: "file-lines", Module: module.Path, Package: "example.com/app/internal/acceptance", File: "current.go", Value: 401, Rationale: "legacy", Phase: "P0"}
	removed := BaselineEntry{Rule: "file-lines", Module: module.Path, Package: "example.com/app/internal/acceptance/removed", File: "old.go", Value: 401, Rationale: "legacy", Phase: "P0"}
	unrelated := BaselineEntry{Rule: "file-lines", Module: module.Path, Package: "example.com/app/internal/services", File: "service.go", Value: 401, Rationale: "legacy", Phase: "P0"}
	baseline := Baseline{Version: baselineVersion, Entries: []BaselineEntry{selected, removed, unrelated}}
	filtered := baselineForScope(baseline, []*Module{module}, []string{"./internal/acceptance/..."})
	issues := compareBaseline([]Issue{{Rule: selected.Rule, Module: selected.Module, Package: selected.Package, File: selected.File, Value: selected.Value}}, filtered)
	if !hasRule(issues, "baseline-stale") {
		t.Fatalf("removed package inside selected prefix was discarded: %#v", filtered)
	}
	for _, issue := range issues {
		if issue.Package == unrelated.Package {
			t.Fatalf("unrelated package leaked into focused baseline report: %#v", issues)
		}
	}
}

func TestUnclassifiedPackagesCannotConsumeLocalServiceInternals(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	for _, test := range []struct {
		name, imported, want string
	}{
		{name: "wire", imported: "example.com/app/services/a/wire", want: "wire-import"},
		{name: "internal", imported: "example.com/app/services/a/internal/impl", want: "peer-private-import"},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := sourceFixture(t, test.name+".go", "package other\nimport \""+test.imported+"\"\n", false)
			pkg := &Package{ImportPath: module.Path + "/other", Dir: "/repo/other", Module: module, Files: []*SourceFile{file}}
			issues := architectureIssues(pkg, module, classifyService(pkg, module, policy), policy)
			if !hasRule(issues, test.want) {
				t.Fatalf("issues = %#v; wanted %s", issues, test.want)
			}
		})
	}
}

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
	baseline := Baseline{Version: baselineVersion, Entries: entries}
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

	baseline := Baseline{Version: baselineVersion, Entries: []BaselineEntry{{
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
