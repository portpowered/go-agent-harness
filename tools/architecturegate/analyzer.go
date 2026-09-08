package main

import (
	"go/ast"
	"go/token"

	"golang.org/x/tools/go/analysis"
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
