package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// MutableGlobalAnalyzer is the package-local go/analysis form of the global
// state rule. The inventory driver applies the same policy with exact manifest
// exceptions so inactive platform files and generated-file registration are
// handled consistently. Keeping the analyzer available also makes the rule
// usable from analysistest and future multi-analyzer runners.
//
//nolint:gochecknoglobals // the exported descriptor is immutable after package initialization
var MutableGlobalAnalyzer = &analysis.Analyzer{
	Name: "architecturemutableglobal",
	Doc:  "reports mutable package variables and package init functions",
	Run:  runMutableGlobalAnalyzer,
}

func runMutableGlobalAnalyzer(pass *analysis.Pass) (interface{}, error) {
	for _, file := range pass.Files {
		for _, declaration := range file.Decls {
			reportGlobalDeclaration(pass, declaration)
		}
	}
	return nil, nil
}

func reportGlobalDeclaration(pass *analysis.Pass, declaration ast.Decl) {
	switch declaration := declaration.(type) {
	case *ast.GenDecl:
		reportGlobalVariables(pass, declaration)
	case *ast.FuncDecl:
		if declaration.Name.Name == "init" && declaration.Recv == nil {
			pass.Reportf(declaration.Pos(), "package init function requires an explicit architecture exception")
		}
	}
}

func reportGlobalVariables(pass *analysis.Pass, declaration *ast.GenDecl) {
	if declaration.Tok != token.VAR {
		return
	}
	for _, specification := range declaration.Specs {
		value, ok := specification.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range value.Names {
			if name.Name != "_" {
				pass.Reportf(name.Pos(), "mutable package variable %s requires an explicit architecture exception", name.Name)
			}
		}
	}
}

// sessionWrapperRule names the architecture issue for a session wrapper that
// hides the session runner's barge-in capabilities.
const sessionWrapperRule = "session-wrapper-capabilities"

// messagesPackagePath owns messages.Session and messages.BargeInCapableSession.
const messagesPackagePath = "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// SessionWrapperAnalyzer reports types that wrap a messages.Session, are
// sessions themselves and do not implement messages.BargeInCapableSession: a
// runner handed such a wrapper cannot see turn detection, local playback, the
// input format or the receive barrier of the provider session beneath it.
//
//nolint:gochecknoglobals // the exported descriptor is immutable after package initialization
var SessionWrapperAnalyzer = &analysis.Analyzer{
	Name: "architecturesessionwrapper",
	Doc:  "reports session wrappers that do not forward the barge-in capabilities",
	Run: func(pass *analysis.Pass) (interface{}, error) {
		for _, wrapper := range sessionWrappersHidingCapabilities(pass.Pkg) {
			pass.Reportf(wrapper.Pos(), "%s", sessionWrapperMessage(wrapper))
		}
		return nil, nil
	},
}

func sessionWrapperMessage(wrapper *types.TypeName) string {
	return fmt.Sprintf("session wrapper %s does not implement messages.BargeInCapableSession; embed messages.SessionCapabilities", wrapper.Name())
}

// sessionWrappersHidingCapabilities returns the struct types of pkg that hold
// a messages.Session, implement messages.Session and do not implement
// messages.BargeInCapableSession.
func sessionWrappersHidingCapabilities(pkg *types.Package) []*types.TypeName {
	session, capable := sessionContracts(pkg)
	if session == nil || capable == nil {
		return nil
	}
	var wrappers []*types.TypeName
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		object, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || object.IsAlias() {
			continue
		}
		structure, ok := object.Type().Underlying().(*types.Struct)
		pointer := types.NewPointer(object.Type())
		if ok && types.Implements(pointer, session) && holdsSession(structure, session) && !types.Implements(pointer, capable) {
			wrappers = append(wrappers, object)
		}
	}
	return wrappers
}

func sessionContracts(pkg *types.Package) (session, capable *types.Interface) {
	messages := messagesPackage(pkg)
	if messages == nil {
		return nil, nil
	}
	return contractInterface(messages, "Session"), contractInterface(messages, "BargeInCapableSession")
}

// messagesPackage finds the messages package pkg depends on, directly or
// through another package (a wrapper may embed a session interface declared
// elsewhere and never import messages). Packages loaded from export data may
// not list their imports, so the struct fields of pkg's types are searched as
// well.
func messagesPackage(pkg *types.Package) *types.Package {
	if found := importedMessages(pkg, map[*types.Package]bool{}); found != nil {
		return found
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		structure, ok := scope.Lookup(name).Type().Underlying().(*types.Struct)
		for index := 0; ok && index < structure.NumFields(); index++ {
			named, isNamed := types.Unalias(structure.Field(index).Type()).(*types.Named)
			if isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == messagesPackagePath {
				return named.Obj().Pkg()
			}
		}
	}
	return nil
}

func importedMessages(pkg *types.Package, visited map[*types.Package]bool) *types.Package {
	if pkg.Path() == messagesPackagePath {
		return pkg
	}
	visited[pkg] = true
	for _, imported := range pkg.Imports() {
		if visited[imported] {
			continue
		}
		if found := importedMessages(imported, visited); found != nil {
			return found
		}
	}
	return nil
}

func contractInterface(pkg *types.Package, name string) *types.Interface {
	object := pkg.Scope().Lookup(name)
	if object == nil {
		return nil
	}
	if contract, ok := object.Type().Underlying().(*types.Interface); ok {
		return contract
	}
	return nil
}

// holdsSession reports whether a field reaches a session: a session
// interface, a concrete session (by value or pointer) or a function returning
// one.
func holdsSession(structure *types.Struct, session *types.Interface) bool {
	for index := range structure.NumFields() {
		field := types.Unalias(structure.Field(index).Type())
		if signature, ok := field.Underlying().(*types.Signature); ok && signature.Results().Len() == 1 {
			field = types.Unalias(signature.Results().At(0).Type())
		}
		if isSession(field, session) {
			return true
		}
	}
	return false
}

func isSession(candidate types.Type, session *types.Interface) bool {
	if types.Implements(candidate, session) {
		return true
	}
	_, pointer := candidate.Underlying().(*types.Pointer)
	return !pointer && !types.IsInterface(candidate) && types.Implements(types.NewPointer(candidate), session)
}

// sessionWrapperIssues reports the session wrappers of pkg that hide the
// runner's barge-in capabilities.
func sessionWrapperIssues(pkg *Package, module *Module) []Issue {
	if pkg.SourceTypes == nil {
		return nil
	}
	wrappers := sessionWrappersHidingCapabilities(pkg.SourceTypes)
	issues := make([]Issue, 0, len(wrappers))
	for _, wrapper := range wrappers {
		issues = append(issues, Issue{Rule: sessionWrapperRule, Module: module.Path, Package: pkg.ImportPath, Symbol: wrapper.Name(), Message: sessionWrapperMessage(wrapper)})
	}
	return issues
}

// loadSessionSourceTypes type-checks, from source, the module's packages that
// reach messages. Session wrappers are usually unexported, and the export
// data the other rules read omits unexported types. Dependencies still come
// from export data, so only these packages are checked from source.
func loadSessionSourceTypes(ctx context.Context, module *Module, reaching map[string]bool, goos, goarch string) error {
	byPath := make(map[string]*Package)
	patterns := make([]string, 0)
	for _, pkg := range module.Packages {
		if pkg.Types != nil && reaching[pkg.ImportPath] {
			byPath[pkg.ImportPath] = pkg
			patterns = append(patterns, pkg.ImportPath)
		}
	}
	if len(patterns) == 0 {
		return nil
	}
	cfg := &packages.Config{
		Context: ctx,
		Mode:    packages.NeedName | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax,
		Dir:     module.Dir,
		Env:     setTypeLoadEnvironment(os.Environ(), goos, goarch),
	}
	loaded, err := packages.Load(cfg, patterns...)
	if err != nil {
		return fmt.Errorf("load session source types for module %q: %w", module.Dir, err)
	}
	for _, source := range loaded {
		if len(source.Errors) > 0 {
			return fmt.Errorf("session source type loading failed for %s: %s", source.PkgPath, formatPackageErrors(source.Errors))
		}
		if pkg := byPath[source.PkgPath]; pkg != nil {
			pkg.SourceTypes = source.Types
		}
	}
	return nil
}

// packagesReachingMessages returns the repository packages that import
// messages directly or through other repository packages; only they can
// declare a type that is a messages.Session.
func packagesReachingMessages(modules []*Module) map[string]bool {
	reaching := map[string]bool{messagesPackagePath: true}
	for changed := true; changed; {
		changed = false
		for _, module := range modules {
			for _, pkg := range module.Packages {
				if !reaching[pkg.ImportPath] && importsAny(pkg.Types, reaching) {
					reaching[pkg.ImportPath], changed = true, true
				}
			}
		}
	}
	return reaching
}

func importsAny(loaded *packages.Package, paths map[string]bool) bool {
	if loaded == nil {
		return false
	}
	for imported := range loaded.Imports {
		if paths[imported] {
			return true
		}
	}
	return false
}
