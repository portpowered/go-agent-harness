package main

import (
	"context"
	"fmt"
	"go/types"
	"os"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

// sessionWrapperRule names the architecture issue for a session wrapper that
// does not relay the wrapped session's optional capabilities through the
// shared forwarder.
const sessionWrapperRule = "session-wrapper-capabilities"

// messagesPackagePath owns messages.Session and messages.SessionCapabilities.
const messagesPackagePath = "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// sessionForwarderName is the shared capability forwarder every session
// wrapper embeds.
const sessionForwarderName = "SessionCapabilities"

// SessionWrapperAnalyzer reports types that hold one or more messages.Session
// values, are sessions themselves and do not embed messages.SessionCapabilities.
// Go cannot type-check that a wrapper forwards every optional capability, and a
// wrapper that copies forwarding methods by hand silently hides each capability
// added after it was written. Embedding the shared forwarder relays every
// capability, present and future, so this is the one property to enforce.
//
//nolint:gochecknoglobals // the exported descriptor is immutable after package initialization
var SessionWrapperAnalyzer = &analysis.Analyzer{
	Name: "architecturesessionwrapper",
	Doc:  "reports session wrappers that do not embed messages.SessionCapabilities",
	Run: func(pass *analysis.Pass) (interface{}, error) {
		for _, wrapper := range sessionWrappersWithoutForwarder(pass.Pkg) {
			pass.Reportf(wrapper.Pos(), "%s", sessionWrapperMessage(wrapper))
		}
		return nil, nil
	},
}

func sessionWrapperMessage(wrapper *types.TypeName) string {
	return fmt.Sprintf("session wrapper %s does not embed messages.SessionCapabilities", wrapper.Name())
}

// sessionWrappersWithoutForwarder returns the struct types of pkg that hold a
// messages.Session (directly, through a function or in a collection),
// implement messages.Session and do not embed messages.SessionCapabilities.
func sessionWrappersWithoutForwarder(pkg *types.Package) []*types.TypeName {
	messages := messagesPackage(pkg)
	if messages == nil {
		return nil
	}
	session := contractInterface(messages, "Session")
	forwarder, ok := messages.Scope().Lookup(sessionForwarderName).(*types.TypeName)
	if session == nil || !ok {
		return nil
	}
	var wrappers []*types.TypeName
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		object, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || object.IsAlias() || object == forwarder {
			continue
		}
		structure, ok := object.Type().Underlying().(*types.Struct)
		if ok && types.Implements(types.NewPointer(object.Type()), session) && holdsSession(structure, session) && !embedsForwarder(object, forwarder) {
			wrappers = append(wrappers, object)
		}
	}
	return wrappers
}

// embedsForwarder reports whether the forwarder is an embedded field of
// wrapper, at any depth, so its methods are promoted.
func embedsForwarder(wrapper, forwarder *types.TypeName) bool {
	field, _, _ := types.LookupFieldOrMethod(wrapper.Type(), true, wrapper.Pkg(), sessionForwarderName)
	variable, ok := field.(*types.Var)
	if !ok || !variable.Embedded() {
		return false
	}
	embedded := types.Unalias(variable.Type())
	if pointer, isPointer := embedded.(*types.Pointer); isPointer {
		embedded = types.Unalias(pointer.Elem())
	}
	named, ok := embedded.(*types.Named)
	return ok && named.Obj() == forwarder
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
// interface, a concrete session (by value or pointer), a function returning
// one, or a slice, array, map or channel of them (a fan-out wrapper).
func holdsSession(structure *types.Struct, session *types.Interface) bool {
	for index := range structure.NumFields() {
		if isSession(heldElement(structure.Field(index).Type()), session) {
			return true
		}
	}
	return false
}

// heldElement returns the type a field holds sessions as: the result of a
// function and the element of a collection.
func heldElement(field types.Type) types.Type {
	field = types.Unalias(field)
	if signature, ok := field.Underlying().(*types.Signature); ok && signature.Results().Len() == 1 {
		field = types.Unalias(signature.Results().At(0).Type())
	}
	switch collection := field.Underlying().(type) {
	case *types.Slice:
		return types.Unalias(collection.Elem())
	case *types.Array:
		return types.Unalias(collection.Elem())
	case *types.Map:
		return types.Unalias(collection.Elem())
	case *types.Chan:
		return types.Unalias(collection.Elem())
	}
	return field
}

func isSession(candidate types.Type, session *types.Interface) bool {
	if types.Implements(candidate, session) {
		return true
	}
	_, pointer := candidate.Underlying().(*types.Pointer)
	return !pointer && !types.IsInterface(candidate) && types.Implements(types.NewPointer(candidate), session)
}

// sessionWrapperIssues reports the session wrappers of pkg that do not embed
// the shared capability forwarder.
func sessionWrapperIssues(pkg *Package, module *Module) []Issue {
	if pkg.SourceTypes == nil {
		return nil
	}
	wrappers := sessionWrappersWithoutForwarder(pkg.SourceTypes)
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
