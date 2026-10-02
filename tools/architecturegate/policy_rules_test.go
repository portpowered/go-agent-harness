package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

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

// TestRepositoryImportRulesRejectViolations proves the forbidden_imports rules
// the gate keeps (glob import patterns depguard cannot express) fail on a
// violation and do not reach beyond their from and production_only scopes.
// TestDepguardImportRulesRejectViolations proves the import rules that
// golangci-lint's depguard owns.
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
	cli, runtime := repo+"/agent-cli", repo+"/go-agent-runtime"
	private, transport := cli+"/internal/services/internal/devices", cli+"/internal/transport/cli"
	return []repositoryImportCase{
		{"tool contract private", cli, cli + "/internal/services/tools", "internal/services/tools/interface.go", cli + "/internal/services/internal/tools", true},
		{"tool contract private elsewhere", cli, cli + "/internal/services/tools", "internal/services/tools/interface.go", cli + "/internal/room/services/internal/state", true},
		{"tool implementation private", cli, cli + "/internal/services/tools", "internal/services/tools/tools.go", cli + "/internal/services/internal/tools", false},
		{"device contract private", cli, cli + "/internal/services/devices", "internal/services/devices/interface.go", private, true},
		{"device probe transport private", cli, transport, "internal/transport/cli/probe_output.go", private, true},
		{"cli transport private", cli, transport + "/internal/livehost", "internal/transport/cli/internal/livehost/files.go", private, true},
		{"cli transport test private", cli, transport, "internal/transport/cli/session_test.go", private, false},
		{"application test runtime access", cli, cli + "/internal/room", "internal/room/room.go", cli + "/internal/services/servicetest", true},
		{"application test runtime access in another module", cli, cli + "/internal/room", "internal/room/room.go", runtime + "/services/servicetest/fake", true},
		{"test runtime access from a test", cli, cli + "/internal/room", "internal/room/room_test.go", cli + "/internal/services/servicetest", false},
		{"runtime entrypoint service wire", runtime, runtime, "runtime.go", runtime + "/services/session/wire", true},
		{"runtime entrypoint nested service wire", runtime, runtime, "runtime.go", runtime + "/services/session/internal/services/turn/wire", true},
		{"runtime entrypoint module wire", runtime, runtime, "runtime.go", runtime + "/wire", false},
		{"runtime package nested service wire", runtime, runtime + "/services/session/wire", "services/session/wire/wire.go", runtime + "/services/turn/wire", true},
		{"application service wire", cli, cli + "/internal/wire", "internal/wire/wire.go", runtime + "/services/session/wire", false},
	}
}

// TestRepositorySourcePatternRulesKeepAudioEncodingInGoAudio proves the
// checked-in forbidden_source_patterns rules reject hand-rolled WAV containers
// and PCM16 packing (under any import alias) outside go-audio, while leaving
// 32-bit fields, the WebP sniffer and go-audio itself alone.
func TestRepositorySourcePatternRulesKeepAudioEncodingInGoAudio(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := loadPolicy("docs/architecture/architecture-policy.json", root)
	if err != nil {
		t.Fatal(err)
	}
	const repo = "github.com/portpowered/go-agent-harness"
	cases := []struct {
		name, module, file, body, rule string
	}{
		{"pcm16 put", repo + "/agent-cli", "internal/room/pcm.go", "var _ = func(b []byte) { binary.LittleEndian.PutUint16(b, 1) }", "hand-rolled-pcm16"},
		{"pcm16 aliased read", repo + "/go-agent-runtime", "services/x/pcm_test.go", "var _ = func(b []byte) uint16 { return le.LittleEndian.Uint16(b) }", "hand-rolled-pcm16"},
		{"riff literal", repo + "/go-agent-loop", "pkg/x/wav.go", `var _ = []byte("RIFF")`, "hand-rolled-wav-container"},
		{"32-bit field", repo + "/agent-cli", "internal/room/size.go", "var _ = func(b []byte) { binary.LittleEndian.PutUint32(b, 1) }", ""},
		{"webp sniffer", repo + "/agent-cli", "internal/input/mimetype.go", `var _ = "RIFF"`, ""},
		{"go-audio owner", repo + "/go-audio", "pkg/wavio/wavio.go", `var _ = func(b []byte) { _ = "RIFF"; binary.LittleEndian.PutUint16(b, 1) }`, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			content := "package x\nimport (\n\t\"encoding/binary\"\n\tle \"encoding/binary\"\n)\nvar _ = binary.LittleEndian\nvar _ = le.LittleEndian\n" + test.body + "\n"
			source := sourceFixture(t, filepath.Base(test.file), content, strings.HasSuffix(test.file, "_test.go"))
			source.RelPath = test.file
			module := &Module{Dir: "/repo", Path: test.module}
			pkg := &Package{ImportPath: test.module + "/" + filepath.Dir(test.file), Module: module}
			issues := sourcePatternIssues(pkg, module, source, policy)
			if test.rule == "" && len(issues) != 0 || test.rule != "" && (len(issues) != 1 || !hasRule(issues, test.rule)) {
				t.Fatalf("issues = %#v, want rule %q", issues, test.rule)
			}
		})
	}
}

// Every type that holds one or more messages.Session values and is itself a
// session must embed the shared capability forwarder.
func TestSessionWrapperAnalyzerFixture(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), SessionWrapperAnalyzer, "architecturesessionwrapper", "architecturesessionwrapperindirect")
}
