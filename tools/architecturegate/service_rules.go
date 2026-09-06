package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"unicode"
)

func architectureIssues(pkg *Package, module *Module, service serviceInfo, policy Policy) []Issue {
	issues := moduleShapeIssues(pkg, module, policy)
	globalState := service.Role != roleNone || matchesAny(policy.GlobalStateScopes, pkg.ImportPath, module.Path)
	for _, source := range pkg.Files {
		issues = append(issues, sourceArchitectureIssues(pkg, module, service, source, policy, globalState)...)
	}
	if service.Role == roleRoot {
		issues = append(issues, serviceInterfaceIssue(pkg, module)...)
	}
	if service.Role == roleRoot || service.Role == roleWire || service.Role == roleComposition || moduleRootAPI(pkg, module, policy) {
		issues = append(issues, publicSurfaceIssues(pkg, module)...)
	}
	return issues
}

func moduleRootAPI(pkg *Package, module *Module, policy Policy) bool {
	return moduleRuleFor(module, policy) != nil && filepath.Clean(pkg.Dir) == filepath.Clean(module.Dir)
}

func moduleShapeIssues(pkg *Package, module *Module, policy Policy) []Issue {
	rule := moduleRuleFor(module, policy)
	if rule == nil || moduleTopLevelAllowed(pkg, module, rule) {
		return nil
	}
	return []Issue{moduleShapeIssue(pkg, module, rule)}
}

func sourceArchitectureIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, policy Policy, globalState bool) []Issue {
	if source.Generated && registeredGenerated(source.Path, module, policy) {
		return nil
	}
	issues := generatedSourceIssues(pkg, module, source, policy)
	issues = append(issues, declarationIssues(pkg, module, service, source, policy, globalState)...)
	issues = append(issues, sourceImportIssues(pkg, module, service, source, policy)...)
	issues = append(issues, serviceRoleIssues(pkg, module, service, source)...)
	issues = append(issues, wireBusinessMethodIssues(pkg, module, service, source)...)
	return issues
}

func generatedSourceIssues(pkg *Package, module *Module, source *SourceFile, policy Policy) []Issue {
	if !hasGeneratedHeader(source.Path) || registeredGenerated(source.Path, module, policy) {
		return nil
	}
	return []Issue{{Rule: "generated-file-spoof", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "generated header is not registered with a reproducible generator"}}
}

func declarationIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, policy Policy, globalState bool) []Issue {
	issues := make([]Issue, 0)
	for _, declaration := range source.AST.Decls {
		issues = append(issues, declarationIssue(pkg, module, service, source, declaration, policy, globalState)...)
	}
	if service.Role == roleRoot && !source.Test {
		issues = append(issues, rootExportIssues(pkg, module, source, policy)...)
	}
	return issues
}

func declarationIssue(pkg *Package, module *Module, service serviceInfo, source *SourceFile, declaration ast.Decl, policy Policy, globalState bool) []Issue {
	switch declaration := declaration.(type) {
	case *ast.GenDecl:
		if declaration.Tok == token.VAR && globalState {
			return globalIssues(pkg, module, source, declaration, policy)
		}
	case *ast.FuncDecl:
		if declaration.Name.Name == "init" && declaration.Recv == nil && globalState {
			return []Issue{{Rule: "init-function", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Symbol: "init", Message: "service packages must not use package initialization"}}
		}
	}
	return nil
}

func serviceRoleIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile) []Issue {
	if service.Role != roleOther {
		return nil
	}
	return []Issue{{Rule: "service-shape", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "service children must be root contracts, internal, wire, or transports"}}
}

// wireBusinessMethodIssues keeps a wire package focused on dependency
// providers and constructors. Exported methods carry behavior on a concrete
// receiver, which means the wire package has become a second implementation
// owner; the behavior belongs under the service's internal package instead.
// Provider functions remain allowed, as do interface method declarations in
// contracts, because neither introduces a concrete implementation receiver.
func wireBusinessMethodIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile) []Issue {
	if service.Role != roleWire || source.Test {
		return nil
	}
	issues := make([]Issue, 0)
	for _, declaration := range source.AST.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || !function.Name.IsExported() {
			continue
		}
		issues = append(issues, Issue{
			Rule:    "wire-business-method",
			Module:  module.Path,
			Package: pkg.ImportPath,
			File:    source.RelPath,
			Symbol:  function.Name.Name,
			Message: "wire packages may provide constructors and providers, but business methods must live behind the service internal implementation",
		})
	}
	return issues
}

func globalIssues(pkg *Package, module *Module, source *SourceFile, declaration *ast.GenDecl, policy Policy) []Issue {
	if source.Generated && registeredGenerated(source.Path, module, policy) && classifyGenerated(source.Path, module, policy) == "wire" {
		return nil
	}
	issues := make([]Issue, 0, len(declaration.Specs))
	for _, spec := range declaration.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		issues = append(issues, globalValueIssues(pkg, module, source, valueSpec, policy)...)
	}
	return issues
}

func globalValueIssues(pkg *Package, module *Module, source *SourceFile, valueSpec *ast.ValueSpec, policy Policy) []Issue {
	issues := make([]Issue, 0, len(valueSpec.Names))
	for _, name := range valueSpec.Names {
		if name.Name == "_" || globalExceptionMatches(pkg, module, source, name.Name, policy) {
			continue
		}
		issues = append(issues, Issue{Rule: "mutable-global", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Symbol: name.Name, Message: "mutable package state must be owned by an injected service"})
	}
	return issues
}

func globalExceptionMatches(pkg *Package, module *Module, source *SourceFile, name string, policy Policy) bool {
	for _, exception := range policy.GlobalExceptions {
		if exceptionMatches(exception, pkg.ImportPath, source, name) {
			return true
		}
	}
	return false
}

func exceptionMatches(exception GlobalException, packagePath string, source *SourceFile, name string) bool {
	if !globMatch(exception.Package, packagePath) || exception.Name != name {
		return false
	}
	return exception.File == "" || globMatch(exception.File, source.RelPath) || globMatch(exception.File, filepath.ToSlash(source.Path))
}

func sourceImportIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, policy Policy) []Issue {
	issues := make([]Issue, 0)
	for _, spec := range source.AST.Imports {
		importPath := strings.Trim(spec.Path.Value, "\"")
		issues = append(issues, importIssues(pkg, module, service, source, importPath, policy)...)
	}
	return issues
}

func importIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, importPath string, policy Policy) []Issue {
	issues := forbiddenImportIssues(pkg, module, source, importPath, policy)
	issues = append(issues, reusableImportIssues(pkg, module, source, importPath, policy)...)
	issues = append(issues, testImportIssues(pkg, module, service, source, importPath)...)
	if service.Role == roleNone && !registeredCompositionSource(pkg, module, source, policy) {
		if moduleOwnsImport(module, importPath) && isServiceWireImport(importPath) {
			issues = append(issues, ownServiceImportIssues(pkg, module, service, source, importPath)...)
		} else if !source.Test && moduleOwnsImport(module, importPath) {
			issues = append(issues, peerServiceImportIssues(pkg, module, service, source, importPath)...)
		}
		return issues
	}
	if registeredCompositionSource(pkg, module, source, policy) {
		service = serviceInfo{Role: roleComposition}
	}
	return append(issues, serviceBoundaryImportIssues(pkg, module, service, source, importPath, policy)...)
}

func forbiddenImportIssues(pkg *Package, module *Module, source *SourceFile, importPath string, policy Policy) []Issue {
	issues := make([]Issue, 0)
	for _, rule := range policy.ForbiddenImports {
		if matchesAny(rule.From, pkg.ImportPath, module.Path) && matchesAny(rule.Imports, importPath) {
			issues = append(issues, Issue{Rule: "forbidden-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: fmt.Sprintf("%s imports %s: %s", pkg.ImportPath, importPath, rule.Reason)})
		}
	}
	return issues
}

func reusableImportIssues(pkg *Package, module *Module, source *SourceFile, importPath string, policy Policy) []Issue {
	if !moduleIsReusable(module, policy) || !matchesAny(policy.CLIModules, importPath) {
		return nil
	}
	return []Issue{{Rule: "reusable-cli-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: fmt.Sprintf("reusable module imports CLI module %s", importPath)}}
}

func testImportIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, importPath string) []Issue {
	imported := serviceImport(importPath)
	if !source.Test || !moduleOwnsImport(module, importPath) || imported.Role != roleInternal || service.Role == roleInternal || service.Role == roleWire {
		return nil
	}
	return []Issue{{Rule: "test-private-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "tests may not bypass service composition to import private implementations"}}
}

func serviceBoundaryImportIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, importPath string, policy Policy) []Issue {
	issues := make([]Issue, 0)
	issues = append(issues, ownServiceImportIssues(pkg, module, service, source, importPath)...)
	issues = append(issues, peerServiceImportIssues(pkg, module, service, source, importPath)...)
	issues = append(issues, forbiddenRootImportIssues(pkg, module, service, source, importPath, policy)...)
	return issues
}

func ownServiceImportIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, importPath string) []Issue {
	issues := make([]Issue, 0)
	if moduleOwnsImport(module, importPath) && service.Role == roleRoot && isSameServiceImplementation(importPath, service) {
		issues = append(issues, Issue{Rule: "root-implementation-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "service root may consume contracts only"})
	}
	if moduleOwnsImport(module, importPath) && service.Role != roleWire && service.Role != roleComposition && isServiceWireImport(importPath) {
		issues = append(issues, Issue{Rule: "wire-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "only service composition packages may import a service wire package"})
	}
	peer := serviceImport(importPath)
	if peer.Name == service.Name && peer.Role == roleInternal && service.Role == roleRoot {
		issues = append(issues, Issue{Rule: "root-private-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "service root cannot import its internal implementation"})
	}
	return issues
}

func peerServiceImportIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, importPath string) []Issue {
	if service.Role == roleComposition || !moduleOwnsImport(module, importPath) {
		return nil
	}
	peer := serviceImport(importPath)
	if peer.Owner == "" || peer.Owner == service.Owner || !privateServiceRole(peer.Role) || nestedWireAllowed(service, peer) {
		return nil
	}
	return []Issue{{Rule: "peer-private-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: fmt.Sprintf("service %s cannot import peer service %s/%s", service.Name, peer.Name, peer.Role)}}
}

func nestedWireAllowed(service, peer serviceInfo) bool {
	if service.Role != roleWire || peer.Role != roleWire {
		return false
	}
	prefix := service.Owner + "/internal/services/"
	return strings.HasPrefix(peer.Owner, prefix) && peer.Owner != service.Owner
}

func privateServiceRole(role serviceRole) bool {
	return role == roleInternal || role == roleWire || role == roleTransport
}

func forbiddenRootImportIssues(pkg *Package, module *Module, service serviceInfo, source *SourceFile, importPath string, policy Policy) []Issue {
	if service.Role != roleRoot {
		return nil
	}
	issues := make([]Issue, 0)
	for _, forbidden := range policy.ForbiddenRootImports {
		if globMatch(forbidden, importPath) {
			issues = append(issues, Issue{Rule: "root-forbidden-import", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Message: "service contract roots cannot import effectful implementation packages"})
		}
	}
	return issues
}

func rootExportIssues(pkg *Package, module *Module, source *SourceFile, policy Policy) []Issue {
	issues := make([]Issue, 0)
	for _, declaration := range source.AST.Decls {
		issues = append(issues, rootDeclarationExports(pkg, module, source, declaration, policy)...)
	}
	return issues
}

func rootDeclarationExports(pkg *Package, module *Module, source *SourceFile, declaration ast.Decl, policy Policy) []Issue {
	if group, ok := declaration.(*ast.GenDecl); ok && group.Tok == token.VAR {
		return rootVariableExports(pkg, module, source, group, policy)
	}
	function, ok := declaration.(*ast.FuncDecl)
	if !ok || function.Recv != nil || !function.Name.IsExported() {
		return nil
	}
	if rootFunctionException(pkg, source, function.Name.Name, policy) {
		return nil
	}
	issues := []Issue{{Rule: "root-exported-function", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Symbol: function.Name.Name, Message: "service root contracts must expose types and values, not free functions"}}
	if isConstructorName(function.Name.Name) {
		issues = append(issues, Issue{Rule: "root-constructor", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Symbol: function.Name.Name, Message: "service root contracts must not expose constructors"})
	}
	return issues
}

func rootVariableExports(pkg *Package, module *Module, source *SourceFile, group *ast.GenDecl, policy Policy) []Issue {
	issues := make([]Issue, 0)
	for _, spec := range group.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, name := range valueSpec.Names {
			if name.IsExported() && rootVariableIsFunction(pkg, valueSpec, name.Name) && !rootFunctionException(pkg, source, name.Name, policy) {
				issues = append(issues, Issue{Rule: "root-exported-function", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Symbol: name.Name, Message: "service root contracts must expose types and values, not free functions"})
			}
		}
	}
	return issues
}

func rootVariableIsFunction(pkg *Package, valueSpec *ast.ValueSpec, name string) bool {
	if valueSpecIsFunction(valueSpec) {
		return true
	}
	if pkg.Types == nil || pkg.Types.Types == nil {
		return false
	}
	object := pkg.Types.Types.Scope().Lookup(name)
	if object == nil {
		return false
	}
	_, ok := types.Unalias(object.Type()).Underlying().(*types.Signature)
	return ok
}

func valueSpecIsFunction(valueSpec *ast.ValueSpec) bool {
	if _, ok := valueSpec.Type.(*ast.FuncType); ok {
		return true
	}
	for _, value := range valueSpec.Values {
		if _, ok := value.(*ast.FuncLit); ok {
			return true
		}
	}
	return false
}

func rootFunctionException(pkg *Package, source *SourceFile, name string, policy Policy) bool {
	for _, exception := range policy.RootFunctionExceptions {
		if exceptionMatches(exception, pkg.ImportPath, source, name) {
			return true
		}
	}
	return false
}

func isConstructorName(name string) bool {
	return strings.HasPrefix(name, "New") && len(name) > 3 && unicode.IsUpper(rune(name[3]))
}

func serviceInterfaceIssue(pkg *Package, module *Module) []Issue {
	if hasServiceInterface(pkg) {
		return nil
	}
	return []Issue{{Rule: "service-interface", Module: module.Path, Package: pkg.ImportPath, Message: "service root must declare an exported Service interface"}}
}

func hasServiceInterface(pkg *Package) bool {
	if pkg.Types != nil && pkg.Types.Types != nil {
		object := pkg.Types.Types.Scope().Lookup("Service")
		if object != nil {
			_, ok := types.Unalias(object.Type()).Underlying().(*types.Interface)
			return ok
		}
	}
	for _, source := range pkg.Files {
		for _, declaration := range source.AST.Decls {
			if serviceInterfaceSyntax(declaration) {
				return true
			}
		}
	}
	return false
}

func serviceInterfaceSyntax(declaration ast.Decl) bool {
	group, ok := declaration.(*ast.GenDecl)
	if !ok || group.Tok != token.TYPE {
		return false
	}
	for _, spec := range group.Specs {
		typeSpec, ok := spec.(*ast.TypeSpec)
		if ok && typeSpec.Name.Name == "Service" {
			_, ok = typeSpec.Type.(*ast.InterfaceType)
			return ok
		}
	}
	return false
}
