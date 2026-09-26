package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// newFixtureFile is the file a fixture entry moves to or is added in.
const newFixtureFile = "new.go"

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

func TestHistoricalBaselineDirectoryMatchesWorkingTreeLoad(t *testing.T) {
	root := t.TempDir()
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	fragments := map[string]BaselineEntry{
		"baselines/example.com/app/app.json":   {Rule: "file-lines", Module: "example.com/app", Package: "example.com/app", File: "app.go", Value: 401, Rationale: "legacy", Phase: "P0"},
		"baselines/example.com/app/b/b.json":   {Rule: "function-lines", Module: "example.com/app", Package: "example.com/app/b", File: "b/b.go", Symbol: "B", Value: 81, Rationale: "legacy", Phase: "P0"},
		"baselines/example.com/zed/zed z.json": {Rule: "file-lines", Module: "example.com/zed", Package: "example.com/zed", File: "zed.go", Value: 402, Rationale: "legacy", Phase: "P0"},
	}
	for path, entry := range fragments {
		data, err := baselineJSON(Baseline{Version: baselineVersion, SourceCommit: "reviewed", Entries: []BaselineEntry{entry}})
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, root, path, string(data))
	}
	writeFixture(t, root, "baselines/README.md", "not a fragment\n")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "sharded baseline")
	historical, found, err := loadHistoricalBaseline(context.Background(), "git", root, "HEAD", "baselines")
	if err != nil || !found {
		t.Fatalf("historical baseline found=%v, err=%v", found, err)
	}
	current, err := loadBaseline("baselines", root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(historical, current) {
		t.Fatalf("historical baseline = %#v\nworking tree baseline = %#v", historical, current)
	}
	added := current
	added.Entries = append(append([]BaselineEntry(nil), current.Entries...), BaselineEntry{Rule: "file-lines", Module: "example.com/app", Package: "example.com/app", File: newFixtureFile, Value: 401, Rationale: "legacy", Phase: "P0"})
	if issues := compareBaselineHistory(context.Background(), "git", root, filepath.Join(root, "baselines"), "HEAD", added); !hasRule(issues, "baseline-history-add") {
		t.Fatalf("added exemption was accepted against a sharded merge-base baseline: %#v", issues)
	}
	raised := current
	raised.Entries = append([]BaselineEntry(nil), current.Entries...)
	raised.Entries[0].Value++
	if issues := compareBaselineHistory(context.Background(), "git", root, filepath.Join(root, "baselines"), "HEAD", raised); !hasRule(issues, "baseline-history-increase") {
		t.Fatalf("raised ceiling was accepted against a sharded merge-base baseline: %#v", issues)
	}
}

func TestHistoricalBaselineDirectoryRejectsCorruptFragment(t *testing.T) {
	root := t.TempDir()
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.email", "architecturegate@example.test")
	gitTestCommand(t, root, "config", "user.name", "architecturegate")
	writeFixture(t, root, "baselines/bad.json", "{")
	gitTestCommand(t, root, "add", ".")
	gitTestCommand(t, root, "commit", "-m", "corrupt baseline")
	if _, _, err := loadHistoricalBaseline(context.Background(), "git", root, "HEAD", "baselines"); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("corrupt fragment error = %v", err)
	}
}

func TestParseGitTreeJSONRejectsNonBlobFragmentsAndMalformedRecords(t *testing.T) {
	blobs, err := parseGitTreeJSON([]byte("100644 blob bbb\tb.json\x00100644 blob ccc\tnotes.md\x00100644 blob aaa\ta.json\x00"))
	if err != nil || len(blobs) != 2 || blobs[0].Path != "a.json" || blobs[1].Object != "bbb" {
		t.Fatalf("parsed blobs = %#v, err=%v", blobs, err)
	}
	if _, err := parseGitTreeJSON([]byte("160000 commit abc\tsub.json\x00")); err == nil {
		t.Fatal("submodule fragment was accepted")
	}
	if _, err := parseGitTreeJSON([]byte("garbage\x00")); err == nil {
		t.Fatal("malformed ls-tree record was accepted")
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

// repositoryImportCase places one import in a package and source file of the
// real workspace policy and states whether a forbidden_imports rule rejects it.
type repositoryImportCase struct {
	name, module, pkg, file, imported string
	rejected                          bool
}

// TestRepositoryImportRulesRejectViolations proves every checked-in
// forbidden_imports rule that replaced an import-scanning test fails on a
// violation, and that its files, production_only and except_from scopes do
// not reach beyond what the retired test checked.
func TestRepositoryImportRulesRejectViolations(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy("docs/architecture/architecture-policy.json", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range repositoryImportCases() {
		t.Run(test.name, func(t *testing.T) {
			module := &Module{Dir: "/repo", Path: test.module}
			pkg := &Package{ImportPath: test.pkg, Module: module}
			source := &SourceFile{RelPath: test.file, Test: strings.HasSuffix(test.file, "_test.go")}
			issues := forbiddenImportIssues(pkg, module, source, test.imported, policy)
			if got := hasRule(issues, "forbidden-import"); got != test.rejected {
				t.Fatalf("%s importing %s: rejected=%v, want %v (issues %#v)", test.file, test.imported, got, test.rejected, issues)
			}
		})
	}
}

func repositoryImportCases() []repositoryImportCase {
	const repo = "github.com/portpowered/go-agent-harness"
	cli, loop := repo+"/agent-cli", repo+"/go-agent-loop"
	gateway, provider := repo+"/go-device-gateway/pkg/devices", repo+"/go-llm-gateway/pkg/providers/openai"
	private := cli + "/internal/services/internal/devices"
	session, devices, transport := cli+"/internal/services/agentsession", cli+"/internal/services/devices", cli+"/internal/transport/cli"
	return []repositoryImportCase{
		{"session contract gateway", cli, session, "internal/services/agentsession/interface.go", gateway, true},
		{"session contract provider", cli, session, "internal/services/agentsession/interface.go", provider, true},
		{"session implementation file", cli, session, "internal/services/agentsession/session.go", gateway, false},
		{"tool contract private", cli, cli + "/internal/services/tools", "internal/services/tools/interface.go", cli + "/internal/services/internal/tools", true},
		{"device contract gateway", cli, devices, "internal/services/devices/interface.go", gateway, true},
		{"device contract private", cli, devices, "internal/services/devices/interface.go", private, true},
		{"device validation file", cli, devices, "internal/services/devices/validation.go", gateway, false},
		{"device list transport", cli, transport, "internal/transport/cli/devices.go", gateway, true},
		{"device probe transport", cli, transport, "internal/transport/cli/probe_output.go", gateway, true},
		{"probe transport", cli, transport, "internal/transport/cli/probe.go", gateway, true},
		{"session transport gateway", cli, transport, "internal/transport/cli/session.go", gateway, false},
		{"cli transport private", cli, transport + "/internal/livehost", "internal/transport/cli/internal/livehost/files.go", private, true},
		{"cli transport test private", cli, transport, "internal/transport/cli/session_test.go", private, false},
		{"loop binary codec", loop, loop + "/pkg/engine", "pkg/engine/engine.go", "encoding/binary", true},
		{"loop platform clock", loop, loop + "/test/functional/media", "test/functional/media/harness.go", loop + "/pkg/platform/clock", true},
		{"loop test binary codec", loop, loop + "/pkg/engine", "pkg/engine/engine_test.go", "encoding/binary", false},
		{"application wav codec", cli, cli + "/internal/room", "internal/room/room.go", repo + "/go-llm-gateway/pkg/wavio", true},
		{"application audio package", cli, cli + "/internal/room", "internal/room/room.go", cli + "/internal/audio/pcm", true},
		{"application test runtime access", cli, cli + "/internal/room", "internal/room/room.go", cli + "/internal/services/servicetest", true},
		{"application binary codec", cli, cli + "/internal/webmcp", "internal/webmcp/frames.go", "encoding/binary", true},
		{"browser testkit identifiers", cli, cli + "/internal/webmcp/testkit", "internal/webmcp/testkit/ids.go", "encoding/binary", false},
		{"application test binary codec", cli, cli + "/internal/room", "internal/room/room_test.go", "encoding/binary", false},
	}
}
