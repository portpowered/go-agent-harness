package main

import (
	"fmt"
	"go/ast"
	"go/types"
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
	for index := 0; index < arguments.Len(); index++ {
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
	for index := 0; index < parameters.Len(); index++ {
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
	for index := 0; index < methods.Len(); index++ {
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
	for index := 0; index < parameters.Len(); index++ {
		if leak := implementationType(parameters.At(index).Constraint(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationStruct(value *types.Struct, visited map[types.Type]bool) string {
	for index := 0; index < value.NumFields(); index++ {
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
	for index := 0; index < value.NumMethods(); index++ {
		method := value.Method(index)
		if !method.Exported() {
			continue
		}
		if leak := implementationType(method.Type(), visited); leak != "" {
			return leak
		}
	}
	for index := 0; index < value.NumEmbeddeds(); index++ {
		if leak := implementationType(value.EmbeddedType(index), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationTuple(tuple *types.Tuple, visited map[types.Type]bool) string {
	for index := 0; index < tuple.Len(); index++ {
		if leak := implementationType(tuple.At(index).Type(), visited); leak != "" {
			return leak
		}
	}
	return ""
}

func implementationUnion(value *types.Union, visited map[types.Type]bool) string {
	for index := 0; index < value.Len(); index++ {
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
