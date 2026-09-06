package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"os"
	"strings"

	"github.com/fzipp/gocyclo"
	"github.com/uudashr/gocognit"
)

func sizeIssues(pkg *Package, module *Module, policy Policy) []Issue {
	issues := make([]Issue, 0)
	maintainedFiles := 0
	for _, source := range pkg.Files {
		if source.Generated && registeredGenerated(source.Path, module, policy) {
			continue
		}
		maintainedFiles++
		issues = append(issues, fileSizeIssues(source, pkg, module, policy.Limits)...)
		issues = append(issues, functionSizeIssues(source, pkg, module, policy.Limits)...)
	}
	if maintainedFiles > policy.Limits.PackageFiles {
		issues = append(issues, Issue{Rule: "package-files", Module: module.Path, Package: pkg.ImportPath, Value: maintainedFiles, Limit: policy.Limits.PackageFiles, Message: "package directory contains too many maintained Go files"})
	}
	return issues
}

func fileSizeIssues(source *SourceFile, pkg *Package, module *Module, limits Limits) []Issue {
	limit := limits.ProductionFileLines
	if source.Test {
		limit = limits.TestFileLines
	}
	lines := physicalLines(source.Path)
	if lines <= limit {
		return nil
	}
	return []Issue{{Rule: "file-lines", Module: module.Path, Package: pkg.ImportPath, File: source.RelPath, Value: lines, Limit: limit, Message: "file exceeds its physical line budget"}}
}

func functionSizeIssues(source *SourceFile, pkg *Package, module *Module, limits Limits) []Issue {
	metricLimits := limitsForSource(source, limits)
	issues := make([]Issue, 0)
	for _, metric := range functionMetrics(source) {
		issues = append(issues, metricIssues(metric, metricLimits, pkg, module, source.RelPath)...)
	}
	return issues
}

type metricLimits struct {
	Lines, Statements, Cognitive, Cyclomatic int
}

func limitsForSource(source *SourceFile, limits Limits) metricLimits {
	if source.Test {
		return metricLimits{limits.TestFunctionLines, limits.TestStatements, limits.TestCognitive, limits.TestCyclomatic}
	}
	return metricLimits{limits.FunctionLines, limits.FunctionStatements, limits.Cognitive, limits.Cyclomatic}
}

func metricIssues(metric functionMetric, limits metricLimits, pkg *Package, module *Module, file string) []Issue {
	issues := make([]Issue, 0)
	if metric.Lines > limits.Lines {
		issues = append(issues, metricIssue("function-lines", metric.Lines, limits.Lines, metric, pkg, module, file, "function exceeds its physical line budget"))
	}
	if metric.Statements > limits.Statements {
		issues = append(issues, metricIssue("function-statements", metric.Statements, limits.Statements, metric, pkg, module, file, "function exceeds its statement budget"))
	}
	if metric.Cognitive > limits.Cognitive {
		issues = append(issues, metricIssue("cognitive-complexity", metric.Cognitive, limits.Cognitive, metric, pkg, module, file, "function exceeds its cognitive complexity budget"))
	}
	if metric.Cyclomatic > limits.Cyclomatic {
		issues = append(issues, metricIssue("cyclomatic-complexity", metric.Cyclomatic, limits.Cyclomatic, metric, pkg, module, file, "function exceeds its cyclomatic complexity budget"))
	}
	return issues
}

func metricIssue(rule string, value, limit int, metric functionMetric, pkg *Package, module *Module, file, message string) Issue {
	return Issue{Rule: rule, Module: module.Path, Package: pkg.ImportPath, File: file, Symbol: metric.Name, Value: value, Limit: limit, Message: message}
}

type functionMetric struct {
	Name, Kind                               string
	Lines, Statements, Cognitive, Cyclomatic int
}

func functionMetrics(source *SourceFile) []functionMetric {
	metrics := make([]functionMetric, 0)
	ast.Inspect(source.AST, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.FuncDecl:
			if node.Body != nil {
				metrics = append(metrics, metricForDeclaration(node, functionName(node, source), source))
			}
		case *ast.FuncLit:
			if node.Body != nil {
				metrics = append(metrics, metricForLiteral(node, literalName(node, source), source))
			}
		}
		return true
	})
	return metrics
}

func metricForDeclaration(node *ast.FuncDecl, name string, source *SourceFile) functionMetric {
	body := functionBody(node)
	return functionMetric{Name: name, Lines: lineSpan(node.Pos(), node.End(), source.Fset), Statements: statementCount(body), Cognitive: gocognit.Complexity(node), Cyclomatic: gocyclo.Complexity(node)}
}

func metricForLiteral(node *ast.FuncLit, name string, source *SourceFile) functionMetric {
	body := functionBody(node)
	// gocognit accepts declarations, so use the literal's real type and body
	// only for this synthetic wrapper. The actual FuncDecl path above is passed
	// through unchanged, preserving receiver and recursion metadata.
	decl := &ast.FuncDecl{Name: ast.NewIdent(name), Type: node.Type, Body: body}
	return functionMetric{Name: name, Lines: lineSpan(node.Pos(), node.End(), source.Fset), Statements: statementCount(body), Cognitive: gocognit.Complexity(decl), Cyclomatic: gocyclo.Complexity(node)}
}

func functionBody(node ast.Node) *ast.BlockStmt {
	switch node := node.(type) {
	case *ast.FuncDecl:
		return node.Body
	case *ast.FuncLit:
		return node.Body
	default:
		return nil
	}
}

func functionName(function *ast.FuncDecl, source *SourceFile) string {
	if function.Recv == nil {
		return function.Name.Name
	}
	return receiverName(function, source.Fset) + "." + function.Name.Name
}

func literalName(literal *ast.FuncLit, source *SourceFile) string {
	position := source.Fset.PositionFor(literal.Pos(), false)
	return fmt.Sprintf("func-literal@%d:%d", position.Line, position.Column)
}

func physicalLines(name string) int {
	data, err := os.ReadFile(name)
	if err != nil || len(data) == 0 {
		return 0
	}
	lines := strings.Count(string(data), "\n")
	if data[len(data)-1] != '\n' {
		lines++
	}
	return lines
}

func lineSpan(start, end token.Pos, fileSet *token.FileSet) int {
	if start == token.NoPos || end == token.NoPos {
		return 0
	}
	startLine, endLine := fileSet.PositionFor(start, false).Line, fileSet.PositionFor(end, false).Line
	if endLine < startLine {
		return 0
	}
	return endLine - startLine + 1
}

func receiverName(function *ast.FuncDecl, fset *token.FileSet) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return ""
	}
	expression := function.Recv.List[0].Type
	var rendered bytes.Buffer
	if err := format.Node(&rendered, fset, expression); err == nil && rendered.Len() > 0 {
		return rendered.String()
	}
	position := fset.PositionFor(expression.Pos(), false)
	return fmt.Sprintf("receiver@%d:%d", position.Line, position.Column)
}

func statementCount(node ast.Node) int {
	count := 0
	ast.Inspect(node, func(node ast.Node) bool {
		if node == nil {
			return false
		}
		switch node.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
			return true
		}
		if _, ok := node.(ast.Stmt); ok {
			count++
		}
		return true
	})
	return count
}
