package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestServiceContractsAndWireStayTypeSafe(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/fixture\n\ngo 1.26.7\n")
	writeFixture(t, root, "services/session/service.go", `package session

import "example.com/fixture/services/session/internal/impl"

type Service interface { Run() error }
type Public struct { Implementation impl.State }
func NewService() impl.State { return impl.State{} }
type PublicBox[T any] struct{}
type PublicAlias = PublicBox[impl.State]
type PublicHandle struct{}
func (*PublicHandle) Leak() impl.State { return impl.State{} }
type Constrained[T interface { Leak() impl.State }] struct{}
type Handler func() impl.State
var Build Handler
`)
	writeFixture(t, root, "services/session/internal/impl/state.go", `package impl
type State struct{}
type Service struct{}
func (Service) Run() error { return nil }
`)
	writeFixture(t, root, "services/session/wire/providers.go", `package wire

import (
  session "example.com/fixture/services/session"
  "example.com/fixture/services/session/internal/impl"
)

func NewService() session.Service { return impl.Service{} }
`)

	policy := fixturePolicy()
	modules, err := discoverModules(context.Background(), "go", root, []string{"."}, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluate(context.Background(), modules, policy, map[string]bool{"architecture": true}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(result.Issues, "root-implementation-import") || !hasRule(result.Issues, "public-implementation-leak") || !hasRule(result.Issues, "root-constructor") || !hasIssueSymbol(result.Issues, "root-exported-function", "Build") {
		t.Fatalf("issues = %#v; wanted root implementation, public leak, and constructor rules", result.Issues)
	}
	for _, symbol := range []string{"PublicAlias", "PublicHandle", "Constrained"} {
		if !hasIssueSymbol(result.Issues, "public-implementation-leak", symbol) {
			t.Fatalf("issues = %#v; generic or pointer leak %q was missed", result.Issues, symbol)
		}
	}
	if hasRuleForPackage(result.Issues, "wire-import", "example.com/fixture/services/session/wire") {
		t.Fatalf("service wire package was incorrectly rejected: %#v", result.Issues)
	}
}

func TestServiceShapeAndPrivateImportRules(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	tests := []struct {
		name       string
		pkgPath    string
		file       string
		content    string
		want       string
		wantImport string
	}{
		{"peer internal", "example.com/app/services/a/internal/service", "a.go", `package service
import "example.com/app/services/b/internal/impl"
`, "peer-private-import", ""},
		{"peer wire", "example.com/app/services/a/internal/service", "a.go", `package service
import "example.com/app/services/b/wire"
`, "peer-private-import", ""},
		{"root wire", "example.com/app/services/a", "a.go", `package a
import "example.com/app/services/a/wire"
`, "wire-import", ""},
		{"external test private", "example.com/app/other", "a_test.go", `package other_test
import "example.com/app/services/a/internal/impl"
`, "test-private-import", ""},
		{"bad child", "example.com/app/services/a/helpers", "a.go", `package helpers
`, "service-shape", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := sourceFixture(t, test.file, test.content, strings.HasSuffix(test.file, "_test.go"))
			relPath := strings.TrimPrefix(test.pkgPath, module.Path+"/")
			pkg := &Package{ImportPath: test.pkgPath, Dir: filepath.Join(module.Dir, relPath), Module: module, Files: []*SourceFile{file}}
			service := classifyService(pkg, module, policy)
			issues := architectureIssues(pkg, module, service, policy)
			if !hasRule(issues, test.want) {
				t.Fatalf("issues = %#v; wanted %s", issues, test.want)
			}
		})
	}
}

func TestNestedServiceOwnershipUsesDeepestRoot(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	parentWireFile := sourceFixture(t, "wire.go", `package wire
import "example.com/app/services/a/internal/services/b/wire"
`, false)
	parentWire := &Package{ImportPath: "example.com/app/services/a/wire", Dir: "/repo/services/a/wire", Module: module, Files: []*SourceFile{parentWireFile}}
	parent := classifyService(parentWire, module, policy)
	if parent.Role != roleWire || parent.Owner != "example.com/app/services/a" {
		t.Fatalf("parent service = %#v", parent)
	}
	if issues := importIssues(parentWire, module, parent, parentWireFile, "example.com/app/services/a/internal/services/b/wire", policy); hasRule(issues, "peer-private-import") {
		t.Fatalf("parent wire could not construct nested wire: %#v", issues)
	}
	nestedRootFile := sourceFixture(t, "service.go", `package service
type Service interface { Run() error }
`, false)
	nestedRoot := &Package{ImportPath: "example.com/app/services/a/internal/services/b", Dir: "/repo/services/a/internal/services/b", Module: module, Files: []*SourceFile{nestedRootFile}}
	nested := classifyService(nestedRoot, module, policy)
	if nested.Role != roleRoot || nested.Name != "b" || nested.Owner != "example.com/app/services/a/internal/services/b" {
		t.Fatalf("nested service = %#v", nested)
	}
	siblingFile := sourceFixture(t, "sibling.go", `package service
import "example.com/app/services/a/internal/services/c/internal/impl"
`, false)
	sibling := &Package{ImportPath: "example.com/app/services/a/internal/services/b/internal/service", Dir: "/repo/services/a/internal/services/b/internal/service", Module: module, Files: []*SourceFile{siblingFile}}
	owner := classifyService(sibling, module, policy)
	issues := importIssues(sibling, module, owner, siblingFile, "example.com/app/services/a/internal/services/c/internal/impl", policy)
	if !hasRule(issues, "peer-private-import") {
		t.Fatalf("nested sibling private import was accepted: %#v", issues)
	}
	legacy := &Package{ImportPath: "example.com/app/internal/services/legacy", Dir: "/repo/internal/services/legacy", Module: module}
	if got := classifyService(legacy, module, policy); got.Role != roleNone {
		t.Fatalf("legacy internal/services path was treated as a service: %#v", got)
	}
	if got := serviceImport("example.com/app/internal/services/legacy/internal/impl"); got.Role != roleNone {
		t.Fatalf("legacy internal/services import was treated as a service: %#v", got)
	}
}

func TestServiceRootsRequireContractInterfaceAndRejectFreeFunctions(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	file := sourceFixture(t, "service.go", `package session
func Run() {}
`, false)
	pkg := &Package{ImportPath: "example.com/app/services/session", Dir: "/repo/services/session", Module: module, Files: []*SourceFile{file}}
	issues := architectureIssues(pkg, module, classifyService(pkg, module, policy), policy)
	if !hasRule(issues, "service-interface") || !hasRule(issues, "root-exported-function") {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestWirePackagesRejectExportedBusinessMethods(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	file := sourceFixture(t, "latency.go", `package wire

type Recorder struct{}

func (Recorder) Observe() {}
func (Recorder) observe() {}
func NewRecorder() Recorder { return Recorder{} }
`, false)
	pkg := &Package{ImportPath: "example.com/app/services/rooms/wire", Dir: "/repo/services/rooms/wire", Module: module, Files: []*SourceFile{file}}
	issues := architectureIssues(pkg, module, classifyService(pkg, module, policy), policy)
	if !hasIssueSymbol(issues, "wire-business-method", "Observe") {
		t.Fatalf("issues = %#v; exported wire business method was accepted", issues)
	}
	if hasIssueSymbol(issues, "wire-business-method", "observe") {
		t.Fatalf("issues = %#v; unexported wire helper was rejected", issues)
	}
	if hasIssueSymbol(issues, "wire-business-method", "NewRecorder") {
		t.Fatalf("issues = %#v; provider constructor was rejected", issues)
	}
}

func TestGlobalStateScopeCoversNonServicePackages(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/audio"}
	policy := fixturePolicy()
	policy.GlobalStateScopes = []string{"example.com/audio/pkg/analysis/**"}
	file := sourceFixture(t, "state.go", "package analysis\nvar DefaultConfig = Config{}\ntype Config struct{}\n", false)
	pkg := &Package{ImportPath: "example.com/audio/pkg/analysis", Dir: "/repo/pkg/analysis", Module: module, Files: []*SourceFile{file}}
	issues := architectureIssues(pkg, module, serviceInfo{}, policy)
	if !hasRule(issues, "mutable-global") {
		t.Fatalf("issues = %#v; scoped package global was missed", issues)
	}
}

func TestReusableModuleCannotImportCLI(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/reusable"}
	policy := fixturePolicy()
	policy.ReusableModules = []string{"example.com/reusable"}
	policy.CLIModules = []string{"example.com/cli/**"}
	file := sourceFixture(t, "reusable.go", `package reusable
import "example.com/cli/internal/config"
var _ config.Config
`, false)
	pkg := &Package{ImportPath: module.Path + "/pkg", Dir: "/repo/pkg", Module: module, Files: []*SourceFile{file}}
	issues := importIssues(pkg, module, serviceInfo{}, file, "example.com/cli/internal/config", policy)
	if !hasRule(issues, "reusable-cli-import") {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestMutableGlobalAnalyzerReportsVariablesAndInit(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", `package fixture
var Registry = map[string]string{}
func init() {}
`, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var reports []analysis.Diagnostic
	_, err = MutableGlobalAnalyzer.Run(&analysis.Pass{Analyzer: MutableGlobalAnalyzer, Fset: fset, Files: []*ast.File{file}, Report: func(d analysis.Diagnostic) { reports = append(reports, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestMutableGlobalAnalyzerFixture(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), MutableGlobalAnalyzer, "architecturemutableglobal")
}

func TestSizeMetricsAndDeletionOnlyBaseline(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "large.go")
	body := "package fixture\nfunc Large() {\n"
	for index := 0; index < 55; index++ {
		body += "_ = " + string(rune('a'+index%26)) + "\n"
	}
	body += "}\n"
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	source := sourceFixtureAt(t, name, body, false)
	pkg := &Package{ImportPath: "example.com/fixture", Dir: dir, Files: []*SourceFile{source}}
	module := &Module{Path: "example.com/fixture", Dir: dir}
	policy := fixturePolicy()
	policy.Limits.FunctionLines = 2
	policy.Limits.FunctionStatements = 2
	issues := sizeIssues(pkg, module, policy)
	if !hasRule(issues, "function-lines") || !hasRule(issues, "function-statements") {
		t.Fatalf("issues = %#v", issues)
	}

	metric := Issue{Rule: "function-lines", Module: "example.com/fixture", Package: "example.com/fixture", File: "large.go", Symbol: "Large", Value: 10, Limit: 2, Message: "too large"}
	baseline := Baseline{Version: baselineVersion, Entries: []BaselineEntry{{Rule: metric.Rule, Module: metric.Module, Package: metric.Package, File: metric.File, Symbol: metric.Symbol, Value: metric.Value, Rationale: "legacy holder", Phase: "P0"}}}
	if got := compareBaseline([]Issue{metric}, baseline); len(got) != 0 {
		t.Fatalf("matching baseline = %#v", got)
	}
	if got := compareBaseline([]Issue{{Rule: metric.Rule, Module: metric.Module, Package: metric.Package, File: metric.File, Symbol: metric.Symbol, Value: 11, Message: metric.Message}}, baseline); !hasRule(got, "baseline-drift") {
		t.Fatalf("increase = %#v", got)
	}
	if got := compareBaseline(nil, baseline); !hasRule(got, "baseline-stale") {
		t.Fatalf("resolved entry = %#v", got)
	}
	if got := compareBaseline([]Issue{{Rule: metric.Rule, Module: metric.Module, Package: metric.Package, File: metric.File, Symbol: metric.Symbol, Value: 9, Message: metric.Message}}, baseline); !hasRule(got, "baseline-drift") {
		t.Fatalf("reduction without baseline edit = %#v", got)
	}
}

func TestBaselineRenameIsOneToOne(t *testing.T) {
	old := BaselineEntry{Rule: "function-lines", Module: "example.com/app", Package: "example.com/app/services/a", File: "old.go", Symbol: "Run", Value: 81, Rationale: "extraction holder", Phase: "P0"}
	current := Issue{Rule: old.Rule, Module: old.Module, Package: old.Package, File: "new.go", Symbol: old.Symbol, Value: old.Value, Message: "same"}
	oldKey := baselineIssue(old).Key()
	newKey := current.Key()
	baseline := Baseline{Version: baselineVersion, Entries: []BaselineEntry{old}, Renames: []BaselineRename{{From: oldKey, To: newKey}}}
	if got := compareBaseline([]Issue{current}, baseline); len(got) != 0 {
		t.Fatalf("renamed holder = %#v", got)
	}
	if err := validateBaseline(baseline); err != nil {
		t.Fatal(err)
	}
	baseline.Renames = append(baseline.Renames, BaselineRename{From: oldKey, To: newKey})
	if err := validateBaseline(baseline); err == nil {
		t.Fatal("duplicate rename was accepted")
	}
}

func TestNestedFunctionLiteralsReceiveIndependentBudgets(t *testing.T) {
	content := `package fixture
func Outer() {
  first := func() { if true { _ = 1 }; if true { _ = 2 }; if true { _ = 3 } }
  second := func() { if true { _ = 4 }; if true { _ = 5 }; if true { _ = 6 } }
  _, _ = first, second
}
var packageHandler = func() { if true { _ = 7 }; if true { _ = 8 }; if true { _ = 9 } }
`
	source := sourceFixture(t, "nested.go", content, false)
	pkg := &Package{ImportPath: "example.com/fixture", Dir: filepath.Dir(source.Path), Files: []*SourceFile{source}}
	module := &Module{Path: "example.com/fixture", Dir: pkg.Dir}
	policy := fixturePolicy()
	policy.Limits.Cognitive = 1
	policy.Limits.Cyclomatic = 1
	issues := sizeIssues(pkg, module, policy)
	if !hasRule(issues, "cognitive-complexity") || !hasRule(issues, "cyclomatic-complexity") {
		t.Fatalf("issues = %#v; nested callbacks were not measured", issues)
	}
	seen := map[string]bool{}
	for _, issue := range issues {
		if (issue.Rule == "cognitive-complexity" || issue.Rule == "cyclomatic-complexity") && strings.HasPrefix(issue.Symbol, "func-literal@") {
			seen[issue.Symbol] = true
		}
	}
	if !seen["func-literal@3:12"] || !seen["func-literal@4:13"] || len(seen) != 3 {
		t.Fatalf("literal identities = %#v; nested and package-level callbacks need stable positions", seen)
	}
}

func TestGenericMethodMetricsHaveDistinctReceiverNames(t *testing.T) {
	content := `package fixture
type First[T any] struct{}
type Second[T any] struct{}
func (First[T]) Run() { if true { _ = 1 } }
func (Second[T]) Run() { if true { _ = 2 } }
`
	source := sourceFixture(t, "generic_methods.go", content, false)
	metrics := functionMetrics(source)
	seen := map[string]bool{}
	for _, metric := range metrics {
		if strings.HasSuffix(metric.Name, ".Run") {
			seen[metric.Name] = true
		}
	}
	if len(seen) != 2 {
		t.Fatalf("method identities = %#v; generic receivers must not collapse to one name", seen)
	}
}

func TestModuleRootAllowlistRejectsCopiedImplementationTree(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/runtime"}
	pkg := &Package{ImportPath: "example.com/runtime/agent", Dir: "/repo/agent", Module: module}
	policy := fixturePolicy()
	policy.ModuleRules = []ModuleRule{{Module: module.Path, AllowedTopLevel: []string{"services", "wire", "platform"}}}
	issues := architectureIssues(pkg, module, serviceInfo{}, policy)
	if !hasRule(issues, "module-root-shape") {
		t.Fatalf("issues = %#v; copied root implementation tree was accepted", issues)
	}
	policy.ModuleRules[0].AllowedTopLevel = append(policy.ModuleRules[0].AllowedTopLevel, ".")
	rootPackage := &Package{ImportPath: module.Path, Dir: module.Dir, Module: module}
	if issues := architectureIssues(rootPackage, module, serviceInfo{}, policy); hasRule(issues, "module-root-shape") {
		t.Fatalf("issues = %#v; explicitly allowed module root was rejected", issues)
	}
	policy.CompositionRoots = append(policy.CompositionRoots, module.Path)
	if got := classifyService(rootPackage, module, policy); got.Role != roleComposition {
		t.Fatalf("module composition root = %#v; exact import path was not recognized", got)
	}
}

func TestCompositionRootsCannotUseWildcardRegistry(t *testing.T) {
	policy := fixturePolicy()
	policy.CompositionRoots = []string{"*/wire"}
	if err := validatePolicy(policy); err == nil {
		t.Fatal("wildcard composition root was accepted")
	}
}

func TestGeneratedRegistrationIsModuleAndHeaderSpecific(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "internal", "wire", "wire_gen.go")
	content := "// Code generated by Wire. DO NOT EDIT.\npackage wire\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	module := &Module{Dir: root, Path: "example.com/agent-cli"}
	policy := fixturePolicy()
	policy.GeneratedFiles = []GeneratedRule{{Module: module.Path, Pattern: "internal/wire/wire_gen.go", Generator: "wire", Header: "// Code generated by Wire. DO NOT EDIT."}}
	if !registeredGenerated(path, module, policy) {
		t.Fatal("exact registered generated file was not accepted")
	}
	wrong := filepath.Join(root, "internal", "other", "wire_gen.go")
	if err := os.MkdirAll(filepath.Dir(wrong), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrong, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if registeredGenerated(wrong, module, policy) {
		t.Fatal("unregistered generated file was accepted")
	}
}

func TestGeneratedHeaderRequiresRegistration(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "spoof.go")
	content := "// Code generated by someone; DO NOT EDIT.\npackage fixture\n"
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	source := sourceFixtureAt(t, name, content, false)
	module := &Module{Path: "example.com/fixture", Dir: dir}
	pkg := &Package{ImportPath: module.Path, Dir: dir, Module: module, Files: []*SourceFile{source}}
	issues := architectureIssues(pkg, module, serviceInfo{}, Policy{Version: policyVersion, Limits: defaultLimits()})
	if !hasRule(issues, "generated-file-spoof") {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestGlobAndMissingModuleValidation(t *testing.T) {
	if !globMatch("**/wire_gen.go", "wire_gen.go") || !globMatch("**/wire_gen.go", "services/a/wire_gen.go") {
		t.Fatal("recursive generated glob did not match")
	}
	if globMatch("services/*", "services/a/internal") {
		t.Fatal("single-segment glob matched recursively")
	}
	root := t.TempDir()
	if _, err := discoverModules(context.Background(), "go", root, []string{"missing"}, []string{"./..."}); err == nil {
		t.Fatal("missing module was accepted")
	}
}

func TestTargetInventoryActivatesPlatformOnlyPackages(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/platform\n\ngo 1.26.7\n")
	writeFixture(t, root, "windows_only.go", "//go:build windows\n\npackage platform\n")
	modules, err := discoverModulesForTarget(context.Background(), "go", root, []string{"."}, []string{"./..."}, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 || len(modules[0].Packages) != 1 || !modules[0].Packages[0].TypeLoadable {
		t.Fatalf("target inventory = %#v; platform-only package was not selected", modules)
	}
}

func fixturePolicy() Policy {
	return Policy{Version: policyVersion, ServiceRoots: []string{"services/*"}, CompositionRoots: []string{"wire"}, Limits: defaultLimits()}
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sourceFixture(t *testing.T, name, content string, test bool) *SourceFile {
	t.Helper()
	return sourceFixtureAt(t, filepath.Join(t.TempDir(), name), content, test)
}

func sourceFixtureAt(t *testing.T, name, content string, test bool) *SourceFile {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, content, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return &SourceFile{Path: name, RelPath: filepath.Base(name), AST: file, Fset: fset, Test: test}
}

func gitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func hasRule(issues []Issue, rule string) bool {
	for _, issue := range issues {
		if issue.Rule == rule {
			return true
		}
	}
	return false
}

func hasRuleForPackage(issues []Issue, rule, pkg string) bool {
	for _, issue := range issues {
		if issue.Rule == rule && issue.Package == pkg {
			return true
		}
	}
	return false
}

func hasIssueSymbol(issues []Issue, rule, symbol string) bool {
	for _, issue := range issues {
		if issue.Rule == rule && issue.Symbol == symbol {
			return true
		}
	}
	return false
}
