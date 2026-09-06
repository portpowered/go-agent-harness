package main

import (
	"context"
	"fmt"
	"go/ast"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type serviceRole string

const (
	roleNone        serviceRole = ""
	roleRoot        serviceRole = "root"
	roleInternal    serviceRole = "internal"
	roleWire        serviceRole = "wire"
	roleComposition serviceRole = "composition"
	roleTransport   serviceRole = "transport"
	roleOther       serviceRole = "other"
)

type serviceInfo struct {
	Name  string
	Root  string
	Owner string
	Role  serviceRole
}

func moduleRuleFor(module *Module, policy Policy) *ModuleRule {
	for index := range policy.ModuleRules {
		rule := &policy.ModuleRules[index]
		if matchesAny([]string{rule.Module}, module.Path, module.Dir, filepath.Base(module.Dir)) {
			return rule
		}
	}
	return nil
}

func moduleTopLevel(pkg *Package, module *Module) string {
	rel, err := filepath.Rel(module.Dir, pkg.Dir)
	if err != nil {
		return ""
	}
	if rel == "." {
		return "."
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func moduleTopLevelAllowed(pkg *Package, module *Module, rule *ModuleRule) bool {
	topLevel := moduleTopLevel(pkg, module)
	for _, allowed := range rule.AllowedTopLevel {
		if allowed == topLevel {
			return true
		}
	}
	return false
}

func moduleShapeIssue(pkg *Package, module *Module, rule *ModuleRule) Issue {
	rel, err := filepath.Rel(module.Dir, pkg.Dir)
	if err != nil {
		rel = pkg.Dir
	}
	return Issue{
		Rule:    "module-root-shape",
		Module:  module.Path,
		Package: pkg.ImportPath,
		File:    filepath.ToSlash(rel),
		Message: fmt.Sprintf("module top-level %q is not in the explicit allowlist", moduleTopLevel(pkg, module)),
	}
}

func evaluate(ctx context.Context, modules []*Module, policy Policy, checks map[string]bool, goos, goarch string) (Result, error) {
	result := inventoryResult(modules, policy)
	if checks["architecture"] {
		if err := loadTypes(ctx, modules, goos, goarch); err != nil {
			return result, err
		}
	}
	result.Issues = append(result.Issues, packageIssues(modules, policy, checks)...)
	result.Sort()
	return result, nil
}

func inventoryResult(modules []*Module, policy Policy) Result {
	var result Result
	for _, module := range modules {
		result.Packages += len(module.Packages)
		for _, pkg := range module.Packages {
			for _, source := range pkg.Files {
				maintained := maintainedFile(source, module, policy)
				result.Files += maintained
				if maintained == 0 {
					continue
				}
				result.Functions += sourceFunctions(source)
			}
		}
	}
	return result
}

func maintainedFile(source *SourceFile, module *Module, policy Policy) int {
	if source.Generated && registeredGenerated(source.Path, module, policy) {
		return 0
	}
	return 1
}

func sourceFunctions(source *SourceFile) int {
	count := 0
	ast.Inspect(source.AST, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncDecl:
			if node.Body != nil {
				count++
			}
		case *ast.FuncLit:
			if node.Body != nil {
				count++
			}
		}
		return true
	})
	return count
}

func packageIssues(modules []*Module, policy Policy, checks map[string]bool) []Issue {
	issues := make([]Issue, 0)
	for _, module := range modules {
		for _, pkg := range module.Packages {
			issues = append(issues, selectedPackageIssues(pkg, module, policy, checks)...)
		}
	}
	return issues
}

func selectedPackageIssues(pkg *Package, module *Module, policy Policy, checks map[string]bool) []Issue {
	issues := make([]Issue, 0)
	if checks["size"] {
		issues = append(issues, sizeIssues(pkg, module, policy)...)
	}
	if checks["architecture"] {
		service := classifyService(pkg, module, policy)
		issues = append(issues, architectureIssues(pkg, module, service, policy)...)
	}
	return issues
}

func classifyService(pkg *Package, module *Module, policy Policy) serviceInfo {
	if registeredCompositionPackage(pkg, module, policy) {
		return serviceInfo{Role: roleComposition}
	}
	rel, err := filepath.Rel(module.Dir, pkg.Dir)
	if err != nil {
		return serviceInfo{}
	}
	rel = filepath.ToSlash(rel)
	if matchesAny(policy.CompositionRoots, rel, pkg.ImportPath) {
		// A module-level runtime/wire package is composition even when the
		// manifest has not yet named it explicitly.
		return serviceInfo{Role: roleComposition}
	}
	if rel == "." {
		return serviceInfo{}
	}
	parts := strings.Split(rel, "/")
	for index := len(parts) - 2; index >= 0; index-- {
		if !isServiceRootSegment(parts, index) || !serviceRootMatches(parts, index, module, policy) {
			continue
		}
		return serviceAt(parts, index, module)
	}
	return serviceInfo{}
}

func serviceImport(importPath string) serviceInfo {
	parts := strings.Split(importPath, "/")
	for index := len(parts) - 2; index >= 0; index-- {
		if isServiceRootSegment(parts, index) {
			return importedServiceAt(parts, index)
		}
	}
	return serviceInfo{}
}

func isSameServiceImplementation(importPath string, service serviceInfo) bool {
	imported := serviceImport(importPath)
	return imported.Owner == service.Owner && privateServiceRole(imported.Role)
}

func isServiceRootSegment(parts []string, index int) bool {
	if index < 0 || index+1 >= len(parts) || parts[index] != "services" || parts[index+1] == "" {
		return false
	}
	if index == 0 {
		return true
	}
	if parts[index-1] == string(roleInternal) {
		return containsServiceSegment(parts[:index])
	}
	return !containsServiceSegment(parts[:index])
}

func containsServiceSegment(parts []string) bool {
	for _, part := range parts {
		if part == "services" {
			return true
		}
	}
	return false
}

func serviceRootMatches(parts []string, index int, module *Module, policy Policy) bool {
	rootRel := strings.Join(parts[:index+2], "/")
	if len(policy.ServiceRoots) == 0 {
		return false
	}
	if matchesAny(policy.ServiceRoots, rootRel, module.Path+"/"+rootRel) {
		return true
	}
	if index > 0 && parts[index-1] == "internal" && containsServiceSegment(parts[:index]) {
		logicalRoot := "services/" + parts[index+1]
		return matchesAny(policy.ServiceRoots, logicalRoot, module.Path+"/"+logicalRoot)
	}
	return false
}

func serviceAt(parts []string, index int, module *Module) serviceInfo {
	rootRel := strings.Join(parts[:index+2], "/")
	service := serviceInfo{Name: parts[index+1], Root: rootRel, Owner: module.Path + "/" + rootRel, Role: roleRoot}
	if len(parts) > index+2 {
		service.Role = serviceRoleForDirectory(parts[index+2])
	}
	return service
}

func importedServiceAt(parts []string, index int) serviceInfo {
	root := strings.Join(parts[:index+2], "/")
	service := serviceInfo{Name: parts[index+1], Root: root, Owner: root, Role: roleRoot}
	if len(parts) > index+2 {
		service.Role = serviceRoleForDirectory(parts[index+2])
	}
	return service
}

func serviceRoleForDirectory(name string) serviceRole {
	switch name {
	case "internal":
		return roleInternal
	case "wire":
		return roleWire
	case "transports":
		return roleTransport
	default:
		return roleOther
	}
}

func isServiceWireImport(importPath string) bool {
	return serviceImport(importPath).Role == roleWire
}

func moduleOwnsImport(module *Module, importPath string) bool {
	if module == nil || strings.TrimSpace(module.Path) == "" {
		return false
	}
	return importPath == module.Path || strings.HasPrefix(importPath, module.Path+"/")
}

func moduleIsReusable(module *Module, policy Policy) bool {
	return matchesAny(policy.ReusableModules, module.Path, module.Dir, filepath.Base(module.Dir))
}

func matchesAny(patterns []string, values ...string) bool {
	for _, pattern := range patterns {
		for _, value := range values {
			if globMatch(pattern, value) {
				return true
			}
		}
	}
	return false
}

func globMatch(pattern, value string) bool {
	pattern, value = filepath.ToSlash(pattern), filepath.ToSlash(value)
	if pattern == value {
		return true
	}
	if strings.HasSuffix(pattern, "/**") && strings.TrimSuffix(pattern, "/**") == value {
		return true
	}
	// path.Match gives familiar single-segment semantics. The small fallback
	// adds ** for recursive import/directory ownership patterns.
	if !strings.Contains(pattern, "**") {
		matched, err := path.Match(pattern, value)
		return err == nil && matched
	}
	expression := recursiveGlobExpression(pattern)
	matched, err := regexp.MatchString("^"+expression.String()+"$", value)
	return err == nil && matched
}

func recursiveGlobExpression(pattern string) strings.Builder {
	var expression strings.Builder
	for index := 0; index < len(pattern); index++ {
		fragment, advance := globFragment(pattern, index)
		expression.WriteString(fragment)
		index += advance
	}
	return expression
}

func globFragment(pattern string, index int) (string, int) {
	if pattern[index] == '*' {
		return starFragment(pattern, index)
	}
	if pattern[index] == '?' {
		return "[^/]", 0
	}
	return regexp.QuoteMeta(string(pattern[index])), 0
}

func starFragment(pattern string, index int) (string, int) {
	if index+1 >= len(pattern) || pattern[index+1] != '*' {
		return "[^/]*", 0
	}
	if index+2 < len(pattern) && pattern[index+2] == '/' {
		return "(?:.*/)?", 2
	}
	return ".*", 1
}
