package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

func publicSurfaceIssues(pkg *Package, module *Module) []Issue {
	if pkg.Types == nil || pkg.Types.Types == nil {
		return nil
	}
	issues := make([]Issue, 0)
	scope := pkg.Types.Types.Scope()
	for _, name := range scope.Names() {
		if !ast.IsExported(name) {
			continue
		}
		object := scope.Lookup(name)
		if object == nil {
			continue
		}
		visited := make(map[types.Type]bool)
		if leak := implementationType(object.Type(), visited); leak != "" {
			issues = append(issues, Issue{Rule: "public-implementation-leak", Module: module.Path, Package: pkg.ImportPath, Symbol: name, Message: fmt.Sprintf("exported API reaches implementation type %s", leak)})
		}
	}
	return issues
}

func implementationType(value types.Type, visited map[types.Type]bool) string {
	if value == nil || visited[value] {
		return ""
	}
	visited[value] = true
	value = types.Unalias(value)
	switch value := value.(type) {
	case *types.Named:
		return implementationNamed(value, visited)
	case *types.Pointer:
		return implementationType(value.Elem(), visited)
	case *types.Slice:
		return implementationType(value.Elem(), visited)
	case *types.Array:
		return implementationType(value.Elem(), visited)
	case *types.Map:
		return implementationMap(value, visited)
	case *types.Chan:
		return implementationType(value.Elem(), visited)
	case *types.Signature:
		return implementationSignature(value, visited)
	case *types.Struct:
		return implementationStruct(value, visited)
	case *types.Interface:
		return implementationInterface(value, visited)
	case *types.Tuple:
		return implementationTuple(value, visited)
	case *types.TypeParam:
		return implementationType(value.Constraint(), visited)
	case *types.Union:
		return implementationUnion(value, visited)
	}
	return ""
}

func implementationNamed(value *types.Named, visited map[types.Type]bool) string {
	if object := value.Obj(); object != nil && object.Pkg() != nil && isImplementationPath(object.Pkg().Path()) {
		return object.String()
	}
	if leak := implementationTypeArguments(value, visited); leak != "" {
		return leak
	}
	if leak := implementationTypeParameters(value, visited); leak != "" {
		return leak
	}
	if leak := implementationType(value.Underlying(), visited); leak != "" {
		return leak
	}
	return implementationMethodSet(value, visited)
}

func implementationTypeArguments(value *types.Named, visited map[types.Type]bool) string {
	arguments := value.TypeArgs()
	if arguments == nil {
		return ""
	}
	for index := range arguments.Len() {
		if leak := implementationType(arguments.At(index), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationTypeParameters(value *types.Named, visited map[types.Type]bool) string {
	parameters := value.TypeParams()
	if parameters == nil {
		return ""
	}
	for index := range parameters.Len() {
		if leak := implementationType(parameters.At(index).Constraint(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationMethodSet(value types.Type, visited map[types.Type]bool) string {
	if leak := implementationMethods(types.NewMethodSet(value), visited); leak != "" {
		return leak
	}
	if named, ok := value.(*types.Named); ok {
		return implementationMethods(types.NewMethodSet(types.NewPointer(named)), visited)
	}
	return ""
}

func implementationMethods(methods *types.MethodSet, visited map[types.Type]bool) string {
	for index := range methods.Len() {
		method := methods.At(index).Obj()
		if !method.Exported() {
			continue
		}
		if leak := implementationType(method.Type(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationMap(value *types.Map, visited map[types.Type]bool) string {
	if leak := implementationType(value.Key(), visited); leak != "" {
		return leak
	}
	return implementationType(value.Elem(), visited)
}

func implementationSignature(value *types.Signature, visited map[types.Type]bool) string {
	if leak := implementationTuple(value.Params(), visited); leak != "" {
		return leak
	}
	if leak := implementationTuple(value.Results(), visited); leak != "" {
		return leak
	}
	parameters := value.TypeParams()
	if parameters == nil {
		return ""
	}
	for index := range parameters.Len() {
		if leak := implementationType(parameters.At(index).Constraint(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationStruct(value *types.Struct, visited map[types.Type]bool) string {
	for index := range value.NumFields() {
		field := value.Field(index)
		if !field.Exported() && !field.Embedded() {
			continue
		}
		if leak := implementationType(field.Type(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationInterface(value *types.Interface, visited map[types.Type]bool) string {
	for index := range value.NumMethods() {
		method := value.Method(index)
		if !method.Exported() {
			continue
		}
		if leak := implementationType(method.Type(), visited); leak != "" {
			return leak
		}
	}
	for index := range value.NumEmbeddeds() {
		if leak := implementationType(value.EmbeddedType(index), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationTuple(tuple *types.Tuple, visited map[types.Type]bool) string {
	for index := range tuple.Len() {
		if leak := implementationType(tuple.At(index).Type(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationUnion(value *types.Union, visited map[types.Type]bool) string {
	for index := range value.Len() {
		if leak := implementationType(value.Term(index).Type(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func isImplementationPath(importPath string) bool {
	parts := strings.Split(importPath, "/")
	for index, part := range parts {
		if part == string(roleInternal) && index > 0 {
			return true
		}
	}
	return false
}

// SourcePatternRule forbids a hand-rolled encoding outside the module that owns
// it. Literals are forbidden substrings of string literals (for example the
// RIFF container identifier); Selectors are forbidden package-qualified
// selector chains written as "<import path>.<Name>[.<Name>...]" (for example
// "encoding/binary.LittleEndian.PutUint16"). A selector matches only when the
// file imports that path, under any local name.
type SourcePatternRule struct {
	Name        string   `json:"name"`
	From        []string `json:"from"`
	ExceptFrom  []string `json:"except_from,omitempty"`
	ExceptFiles []string `json:"except_files,omitempty"`
	Literals    []string `json:"literals,omitempty"`
	Selectors   []string `json:"selectors,omitempty"`
	Reason      string   `json:"reason"`
}

func (rule SourcePatternRule) appliesTo(pkg *Package, module *Module, source *SourceFile) bool {
	if !matchesAny(rule.From, pkg.ImportPath, module.Path) || matchesAny(rule.ExceptFrom, pkg.ImportPath, module.Path) {
		return false
	}
	return !matchesAny(rule.ExceptFiles, source.RelPath)
}

func sourcePatternIssues(pkg *Package, module *Module, source *SourceFile, policy Policy) []Issue {
	issues := make([]Issue, 0)
	for _, rule := range policy.ForbiddenSourcePatterns {
		if !rule.appliesTo(pkg, module, source) {
			continue
		}
		selectors := rule.localSelectors(source.AST)
		ast.Inspect(source.AST, func(node ast.Node) bool {
			if match, ok := rule.match(node, selectors); ok {
				issues = append(issues, Issue{
					Rule: rule.Name, Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Symbol: match,
					Message: fmt.Sprintf("line %d uses %s: %s", source.Fset.Position(node.Pos()).Line, match, rule.Reason),
				})
				return false
			}
			return true
		})
	}
	return issues
}

// localSelectors rewrites each forbidden selector onto the local import name
// the file uses, dropping selectors whose package the file does not import.
func (rule SourcePatternRule) localSelectors(file *ast.File) map[string]string {
	local := make(map[string]string, len(rule.Selectors))
	for _, selector := range rule.Selectors {
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil || !strings.HasPrefix(selector, path+".") {
				continue
			}
			name := path[strings.LastIndex(path, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			local[name+strings.TrimPrefix(selector, path)] = selector
		}
	}
	return local
}

func (rule SourcePatternRule) match(node ast.Node, selectors map[string]string) (string, bool) {
	switch node := node.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(node.Value)
		if err != nil {
			return "", false
		}
		for _, literal := range rule.Literals {
			if strings.Contains(value, literal) {
				return strconv.Quote(literal), true
			}
		}
	case *ast.SelectorExpr:
		if selector, ok := selectors[selectorChain(node)]; ok {
			return selector, true
		}
	}
	return "", false
}

func selectorChain(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		prefix := selectorChain(expression.X)
		if prefix == "" {
			return ""
		}
		return prefix + "." + expression.Sel.Name
	default:
		return ""
	}
}
