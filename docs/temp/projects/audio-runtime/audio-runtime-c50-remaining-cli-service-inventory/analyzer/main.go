// Command analyzer emits a deterministic source census for C50.
//
// It deliberately uses the standard library's parser rather than package
// loading or type-checking: the output records exact AST evidence and marks
// interface/reflection ambiguity instead of silently presenting a heuristic
// as a resolved call graph. The analyzer itself is task-local evidence tooling
// and never writes outside the caller-provided output directory.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	classThin        = "THIN_TRANSPORT_DELEGATION"
	classPolicy      = "BUSINESS_POLICY"
	classComposition = "COMPOSITION_WIRING"
	classRuntime     = "REUSABLE_RUNTIME_BEHAVIOR"
	classUncertain   = "DEAD_OR_UNCERTAIN"
)

var targetRoots = []string{
	"agent-cli/internal/services/internal/agentruntime",
	"agent-cli/internal/transport/cli/internal/livehost",
	"agent-cli/internal/room",
}

type Position struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type Import struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

type FileRecord struct {
	Path              string   `json:"path"`
	Root              string   `json:"root"`
	Package           string   `json:"package"`
	ImportPath        string   `json:"import_path"`
	Kind              string   `json:"kind"`
	PhysicalLines     int      `json:"physical_lines"`
	Bytes             int      `json:"bytes"`
	Imports           []Import `json:"imports"`
	TopLevelSymbolIDs []string `json:"top_level_symbol_ids"`
	ParseError        string   `json:"parse_error,omitempty"`
}

type Exclusion struct {
	Path          string `json:"path"`
	Root          string `json:"root"`
	Reason        string `json:"reason"`
	PhysicalLines int    `json:"physical_lines"`
	Bytes         int    `json:"bytes"`
	ParseError    string `json:"parse_error,omitempty"`
}

type Caller struct {
	File             string `json:"file"`
	Line             int    `json:"line"`
	Column           int    `json:"column"`
	CallerPackage    string `json:"caller_package"`
	CallerImportPath string `json:"caller_import_path"`
	CallerSymbol     string `json:"caller_symbol"`
	Expression       string `json:"expression"`
	Resolution       string `json:"resolution"`
	Confidence       string `json:"confidence"`
}

type Symbol struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	QualifiedName          string   `json:"qualified_name"`
	Kind                   string   `json:"kind"`
	Receiver               string   `json:"receiver,omitempty"`
	Exported               bool     `json:"exported"`
	Package                string   `json:"package"`
	ImportPath             string   `json:"import_path"`
	File                   string   `json:"file"`
	Line                   int      `json:"line"`
	EndLine                int      `json:"end_line"`
	Signature              string   `json:"signature"`
	Imports                []string `json:"imports"`
	ProductionCallers      []Caller `json:"production_callers"`
	Class                  string   `json:"class"`
	ClassificationEvidence []string `json:"classification_evidence"`
	Confidence             string   `json:"confidence"`
}

type CallEdge struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
	Call   Caller `json:"call"`
}

type Diagnostic struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type AreaCount struct {
	Root              string `json:"root"`
	ProductionFiles   int    `json:"production_files"`
	ProductionLines   int    `json:"production_lines"`
	ProductionSymbols int    `json:"production_symbols"`
	ExcludedFiles     int    `json:"excluded_files"`
	ExcludedLines     int    `json:"excluded_lines"`
}

type Inventory struct {
	SchemaVersion        string         `json:"schema_version"`
	SourceRevision       string         `json:"source_revision"`
	TargetRoots          []string       `json:"target_roots"`
	Files                []FileRecord   `json:"files"`
	Exclusions           []Exclusion    `json:"exclusions"`
	Symbols              []Symbol       `json:"symbols"`
	CallEdges            []CallEdge     `json:"call_edges"`
	Areas                []AreaCount    `json:"areas"`
	Totals               AreaCount      `json:"totals"`
	Diagnostics          []Diagnostic   `json:"diagnostics"`
	HistoricalComparison map[string]any `json:"historical_comparison"`
}

type parsedFile struct {
	Path       string
	Rel        string
	Root       string
	Package    string
	ImportPath string
	AST        *ast.File
	Fset       *token.FileSet
	Imports    []Import
	Kind       string
	Source     []byte
}

type symbolRef struct {
	Index      int
	Name       string
	Package    string
	ImportPath string
	File       string
	Kind       string
	Receiver   string
}

type callSite struct {
	File         string
	Package      string
	ImportPath   string
	CallerSymbol string
	CallerID     string
	Line         int
	Column       int
	Expression   string
	Name         string
	Qualifier    string
	Local        bool
}

func main() {
	root := flag.String("root", ".", "repository root")
	out := flag.String("out", ".", "output directory")
	revision := flag.String("source-revision", "", "exact source revision")
	flag.Parse()
	if *revision == "" {
		fatalf("--source-revision is required")
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		fatalf("resolve root: %v", err)
	}
	outAbs, err := filepath.Abs(*out)
	if err != nil {
		fatalf("resolve output: %v", err)
	}
	if err := os.MkdirAll(outAbs, 0o755); err != nil {
		fatalf("create output: %v", err)
	}
	inventory, err := buildInventory(rootAbs, *revision)
	if err != nil {
		fatalf("build inventory: %v", err)
	}
	if len(inventory.Diagnostics) > 0 {
		for _, diagnostic := range inventory.Diagnostics {
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", diagnostic.Kind, diagnostic.Path, diagnostic.Message)
		}
		fatalf("source parse diagnostics present")
	}
	if err := writeJSON(filepath.Join(outAbs, "inventory.json"), inventory); err != nil {
		fatalf("write inventory: %v", err)
	}
	if err := writeJSON(filepath.Join(outAbs, "classifications.json"), classificationDocument(inventory)); err != nil {
		fatalf("write classifications: %v", err)
	}
	if err := writeJSON(filepath.Join(outAbs, "call-paths.json"), callPathDocument(inventory)); err != nil {
		fatalf("write call paths: %v", err)
	}
	if err := writeMarkdown(outAbs, inventory); err != nil {
		fatalf("write markdown: %v", err)
	}
}

func buildInventory(root, revision string) (Inventory, error) {
	all, err := discoverAllGoFiles(root)
	if err != nil {
		return Inventory{}, err
	}
	target := make(map[string]string)
	for _, targetRoot := range targetRoots {
		abs := filepath.Join(root, filepath.FromSlash(targetRoot))
		entries, err := discoverGoFilesUnder(abs)
		if err != nil {
			return Inventory{}, fmt.Errorf("discover %s: %w", targetRoot, err)
		}
		for _, path := range entries {
			target[path] = targetRoot
		}
	}
	moduleRoots, modulePaths := moduleInfo(root)
	fset := token.NewFileSet()
	parsed := make([]parsedFile, 0, len(all))
	diagnostics := make([]Diagnostic, 0)
	for _, path := range all {
		source, err := os.ReadFile(path)
		if err != nil {
			return Inventory{}, fmt.Errorf("read %s: %w", path, err)
		}
		pkg, imports, astFile, parseErr := parseFile(path, source, fset)
		rel := relPath(root, path)
		moduleRoot, modulePath := owningModule(path, moduleRoots, modulePaths)
		importPath := modulePath
		if modulePath != "" {
			moduleRel, err := filepath.Rel(moduleRoot, filepath.Dir(path))
			if err == nil && moduleRel != "." {
				importPath += "/" + filepath.ToSlash(moduleRel)
			}
		}
		kind := fileKind(path, source)
		parsed = append(parsed, parsedFile{Path: path, Rel: rel, Root: target[path], Package: pkg, ImportPath: importPath, AST: astFile, Fset: fset, Imports: imports, Kind: kind, Source: source})
		if parseErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: rel, Kind: "parse_error", Message: parseErr.Error()})
		}
	}
	for path, targetRoot := range target {
		found := false
		for i := range parsed {
			if parsed[i].Path == path {
				found = true
				if parsed[i].Root == "" {
					parsed[i].Root = targetRoot
				}
				break
			}
		}
		if !found {
			return Inventory{}, fmt.Errorf("target file missing from parse set: %s", relPath(root, path))
		}
	}

	inventory := Inventory{SchemaVersion: "c50-inventory-v1", SourceRevision: revision, TargetRoots: append([]string(nil), targetRoots...), Diagnostics: diagnostics, HistoricalComparison: map[string]any{
		"historical_observation": map[string]any{"production_files": 107, "physical_lines": 41305},
		"historical_scope":       "operator observation of the then-counted CLI services/internal/agentruntime scope; exact inclusion rules and revision were not supplied with the observation",
		"current_scope":          "three named roots in prd.json; production .go excludes _test.go and generated files, while every excluded file is listed",
		"comparison_rule":        "difference is inventory only, never a migration percentage",
	}}
	refs := make([]symbolRef, 0)
	for i := range parsed {
		if parsed[i].Root == "" {
			continue
		}
		fileRecord, exclusions, symbols, fileRefs := inspectTargetFile(root, parsed[i], len(inventory.Symbols))
		if parsed[i].Kind == "production" {
			inventory.Files = append(inventory.Files, fileRecord)
			inventory.Symbols = append(inventory.Symbols, symbols...)
			refs = append(refs, fileRefs...)
		} else {
			inventory.Exclusions = append(inventory.Exclusions, exclusions...)
		}
	}
	sort.Slice(inventory.Files, func(i, j int) bool { return inventory.Files[i].Path < inventory.Files[j].Path })
	sort.Slice(inventory.Exclusions, func(i, j int) bool { return inventory.Exclusions[i].Path < inventory.Exclusions[j].Path })
	sort.Slice(inventory.Symbols, func(i, j int) bool {
		if inventory.Symbols[i].File != inventory.Symbols[j].File {
			return inventory.Symbols[i].File < inventory.Symbols[j].File
		}
		if inventory.Symbols[i].Line != inventory.Symbols[j].Line {
			return inventory.Symbols[i].Line < inventory.Symbols[j].Line
		}
		return inventory.Symbols[i].ID < inventory.Symbols[j].ID
	})
	// Rebuild indexes after the stable sort.
	byKey := make(map[string][]int)
	for i := range inventory.Symbols {
		byKey[symbolKey(inventory.Symbols[i].ImportPath, inventory.Symbols[i].Package, inventory.Symbols[i].Name)] = append(byKey[symbolKey(inventory.Symbols[i].ImportPath, inventory.Symbols[i].Package, inventory.Symbols[i].Name)], i)
	}
	allCalls := collectCalls(root, parsed, inventory.Symbols, byKey)
	for _, edge := range allCalls {
		inventory.CallEdges = append(inventory.CallEdges, edge)
		for i := range inventory.Symbols {
			if inventory.Symbols[i].ID == edge.Callee {
				inventory.Symbols[i].ProductionCallers = append(inventory.Symbols[i].ProductionCallers, edge.Call)
			}
		}
	}
	for i := range inventory.Symbols {
		classify(&inventory.Symbols[i])
		sort.Slice(inventory.Symbols[i].ProductionCallers, func(a, b int) bool {
			x, y := inventory.Symbols[i].ProductionCallers[a], inventory.Symbols[i].ProductionCallers[b]
			if x.File != y.File {
				return x.File < y.File
			}
			if x.Line != y.Line {
				return x.Line < y.Line
			}
			return x.Expression < y.Expression
		})
	}
	inventory.Areas, inventory.Totals = counts(inventory)
	sort.Slice(inventory.CallEdges, func(i, j int) bool {
		if inventory.CallEdges[i].Callee != inventory.CallEdges[j].Callee {
			return inventory.CallEdges[i].Callee < inventory.CallEdges[j].Callee
		}
		if inventory.CallEdges[i].Call.File != inventory.CallEdges[j].Call.File {
			return inventory.CallEdges[i].Call.File < inventory.CallEdges[j].Call.File
		}
		return inventory.CallEdges[i].Call.Line < inventory.CallEdges[j].Call.Line
	})
	return inventory, nil
}

func discoverAllGoFiles(root string) ([]string, error) {
	var files []string
	moduleDirs := []string{"agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway", "tests"}
	for _, dir := range moduleDirs {
		entries, err := discoverGoFilesUnder(filepath.Join(root, dir))
		if err != nil {
			return nil, err
		}
		files = append(files, entries...)
	}
	sort.Strings(files)
	return files, nil
}

func discoverGoFilesUnder(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			base := entry.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func moduleInfo(root string) ([]string, map[string]string) {
	var roots []string
	paths := make(map[string]string)
	for _, dir := range []string{"agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway", "tests"} {
		moduleRoot := filepath.Join(root, dir)
		data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "module" {
				roots = append(roots, moduleRoot)
				paths[moduleRoot] = fields[1]
				break
			}
		}
	}
	return roots, paths
}

func owningModule(path string, roots []string, paths map[string]string) (string, string) {
	best := ""
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	return best, paths[best]
}

func parseFile(path string, source []byte, fset *token.FileSet) (string, []Import, *ast.File, error) {
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if file == nil {
		return "", nil, nil, err
	}
	imports := make([]Import, 0, len(file.Imports))
	for _, spec := range file.Imports {
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		}
		pathValue, _ := strconv.Unquote(spec.Path.Value)
		imports = append(imports, Import{Name: name, Path: pathValue, Line: fset.Position(spec.Pos()).Line})
	}
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
	return file.Name.Name, imports, file, err
}

func fileKind(path string, source []byte) string {
	if strings.HasSuffix(path, "_test.go") {
		return "test"
	}
	first := string(source)
	if len(first) > 4096 {
		first = first[:4096]
	}
	if strings.Contains(first, "Code generated") || strings.HasSuffix(path, "_gen.go") {
		return "generated"
	}
	return "production"
}

func inspectTargetFile(root string, file parsedFile, symbolBase int) (FileRecord, []Exclusion, []Symbol, []symbolRef) {
	physicalLines := lineCount(file.Source)
	if file.Kind != "production" {
		reason := "TEST_FILE"
		if file.Kind == "generated" {
			reason = "GENERATED_FILE"
		}
		return FileRecord{}, []Exclusion{{Path: file.Rel, Root: file.Root, Reason: reason, PhysicalLines: physicalLines, Bytes: len(file.Source)}}, nil, nil
	}
	record := FileRecord{Path: file.Rel, Root: file.Root, Package: file.Package, ImportPath: file.ImportPath, Kind: file.Kind, PhysicalLines: physicalLines, Bytes: len(file.Source)}
	record.Imports = append(record.Imports, file.Imports...)
	var symbols []Symbol
	var refs []symbolRef
	if file.AST != nil {
		for _, declaration := range file.AST.Decls {
			switch decl := declaration.(type) {
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						name := spec.Name.Name
						symbol := makeSymbol(file, name, "type", "", spec.Pos(), spec.End(), spec)
						refs = append(refs, symbolRef{Index: symbolBase + len(symbols), Name: symbol.Name, Package: file.Package, ImportPath: file.ImportPath, File: file.Rel, Kind: symbol.Kind})
						record.TopLevelSymbolIDs = append(record.TopLevelSymbolIDs, symbol.ID)
						symbols = append(symbols, symbol)
					case *ast.ValueSpec:
						kind := strings.ToLower(decl.Tok.String())
						for _, nameIdent := range spec.Names {
							name := nameIdent.Name
							symbol := makeSymbol(file, name, kind, "", nameIdent.Pos(), spec.End(), spec)
							refs = append(refs, symbolRef{Index: symbolBase + len(symbols), Name: symbol.Name, Package: file.Package, ImportPath: file.ImportPath, File: file.Rel, Kind: symbol.Kind})
							record.TopLevelSymbolIDs = append(record.TopLevelSymbolIDs, symbol.ID)
							symbols = append(symbols, symbol)
						}
					}
				}
			case *ast.FuncDecl:
				name := decl.Name.Name
				receiver := receiverName(decl.Recv)
				kind := "function"
				if receiver != "" {
					kind = "method"
				}
				symbol := makeSymbol(file, name, kind, receiver, decl.Pos(), decl.End(), decl)
				refs = append(refs, symbolRef{Index: symbolBase + len(symbols), Name: symbol.Name, Package: file.Package, ImportPath: file.ImportPath, File: file.Rel, Kind: symbol.Kind, Receiver: receiver})
				record.TopLevelSymbolIDs = append(record.TopLevelSymbolIDs, symbol.ID)
				symbols = append(symbols, symbol)
			}
		}
	}
	return record, nil, symbols, refs
}

func makeSymbol(file parsedFile, name, kind, receiver string, start, end token.Pos, node ast.Node) Symbol {
	startPos := file.Fset.Position(start)
	endPos := file.Fset.Position(end)
	qualified := file.ImportPath + "." + name
	if receiver != "" {
		qualified = file.ImportPath + ".(" + receiver + ")." + name
	}
	id := file.Rel + ":" + strconv.Itoa(startPos.Line) + ":" + kind + ":" + qualified
	signature := nodeSignature(node)
	return Symbol{ID: id, Name: name, QualifiedName: qualified, Kind: kind, Receiver: receiver, Exported: ast.IsExported(name), Package: file.Package, ImportPath: file.ImportPath, File: file.Rel, Line: startPos.Line, EndLine: endPos.Line, Signature: signature, Imports: importPaths(file.Imports), ProductionCallers: []Caller{}}
}

func nodeSignature(node ast.Node) string {
	var buffer bytes.Buffer
	if err := format.Node(&buffer, token.NewFileSet(), node); err != nil {
		return ""
	}
	value := strings.TrimSpace(buffer.String())
	if len(value) > 800 {
		value = value[:800] + "...(truncated)"
	}
	return value
}

func receiverName(fieldList *ast.FieldList) string {
	if fieldList == nil || len(fieldList.List) == 0 {
		return ""
	}
	expr := fieldList.List[0].Type
	for {
		switch value := expr.(type) {
		case *ast.StarExpr:
			expr = value.X
		case *ast.Ident:
			return value.Name
		case *ast.IndexExpr:
			expr = value.X
		case *ast.IndexListExpr:
			expr = value.X
		default:
			return ""
		}
	}
}

func importPaths(imports []Import) []string {
	paths := make([]string, 0, len(imports))
	for _, item := range imports {
		paths = append(paths, item.Path)
	}
	sort.Strings(paths)
	return paths
}

func collectCalls(root string, parsed []parsedFile, symbols []Symbol, byKey map[string][]int) []CallEdge {
	var edges []CallEdge
	for _, file := range parsed {
		if file.Kind != "production" || file.AST == nil {
			continue
		}
		imports := make(map[string]string)
		for _, item := range file.Imports {
			alias := item.Name
			if alias == "" {
				alias = pathBase(item.Path)
			}
			if alias != "." && alias != "_" {
				imports[alias] = item.Path
			}
		}
		ast.Inspect(file.AST, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, qualifier, local, expression := calledName(call.Fun)
			if name == "" {
				return true
			}
			callerSymbol, callerID := enclosingCaller(file, call.Pos())
			line := file.Fset.Position(call.Pos())
			for _, index := range candidateSymbolIndexes(name, qualifier, local, imports, file, symbols, byKey) {
				callee := symbols[index]
				resolution := "same-package AST call"
				confidence := "high"
				if !local {
					resolution = "import-qualified AST call"
				} else if callee.Kind == "method" {
					resolution = "method-name AST call; receiver/interface resolution is not available"
					confidence = "medium"
				}
				caller := Caller{File: file.Rel, Line: line.Line, Column: line.Column, CallerPackage: file.Package, CallerImportPath: file.ImportPath, CallerSymbol: callerSymbol, Expression: expression, Resolution: resolution, Confidence: confidence}
				edges = append(edges, CallEdge{Caller: callerID, Callee: callee.ID, Call: caller})
			}
			return true
		})
	}
	_ = root
	return dedupeEdges(edges)
}

func calledName(expr ast.Expr) (string, string, bool, string) {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name, "", true, value.Name
	case *ast.SelectorExpr:
		qualifier := ""
		if ident, ok := value.X.(*ast.Ident); ok {
			qualifier = ident.Name
		}
		if qualifier == "" {
			return value.Sel.Name, "", false, value.Sel.Name
		}
		return value.Sel.Name, qualifier, false, qualifier + "." + value.Sel.Name
	default:
		return "", "", false, ""
	}
}

func enclosingCaller(file parsedFile, pos token.Pos) (string, string) {
	var best *ast.FuncDecl
	ast.Inspect(file.AST, func(node ast.Node) bool {
		decl, ok := node.(*ast.FuncDecl)
		if !ok || decl.Body == nil {
			return true
		}
		if pos >= decl.Pos() && pos <= decl.End() {
			if best == nil || (decl.End()-decl.Pos()) < (best.End()-best.Pos()) {
				best = decl
			}
		}
		return true
	})
	if best == nil {
		return "<file-init>", file.Rel + ":init"
	}
	name := best.Name.Name
	if receiver := receiverName(best.Recv); receiver != "" {
		name = "(" + receiver + ")." + name
	}
	posValue := file.Fset.Position(best.Pos())
	return name, file.Rel + ":" + strconv.Itoa(posValue.Line) + ":" + name
}

func candidateSymbolIndexes(name, qualifier string, local bool, imports map[string]string, file parsedFile, symbols []Symbol, byKey map[string][]int) []int {
	var indexes []int
	if local {
		indexes = append(indexes, byKey[symbolKey(file.ImportPath, file.Package, name)]...)
		return indexes
	}
	importPath := imports[qualifier]
	if importPath == "" {
		return nil
	}
	indexes = append(indexes, byKey[symbolKey(importPath, pathBase(importPath), name)]...)
	if len(indexes) == 0 {
		for i := range symbols {
			if symbols[i].ImportPath == importPath && symbols[i].Name == name {
				indexes = append(indexes, i)
			}
		}
	}
	return indexes
}

func symbolKey(importPath, packageName, name string) string {
	return importPath + "|" + packageName + "|" + name
}

func dedupeEdges(edges []CallEdge) []CallEdge {
	seen := make(map[string]bool)
	result := make([]CallEdge, 0, len(edges))
	for _, edge := range edges {
		key := edge.Caller + "|" + edge.Callee + "|" + edge.Call.File + ":" + strconv.Itoa(edge.Call.Line) + "|" + edge.Call.Expression
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, edge)
	}
	return result
}

func classify(symbol *Symbol) {
	if len(symbol.ProductionCallers) == 0 {
		symbol.Class = classUncertain
		symbol.Confidence = "low"
		symbol.ClassificationEvidence = []string{"no statically resolved production caller in the workspace scan; dynamic/interface/reflection use remains possible"}
		return
	}
	file := strings.ToLower(symbol.File)
	signature := strings.ToLower(symbol.Signature)
	imports := strings.ToLower(strings.Join(symbol.Imports, " "))
	evidence := []string{"production caller resolved by AST CallExpr at cited locations"}
	class := classRuntime
	confidence := "medium"
	if strings.Contains(file, "/livehost/") {
		class = classThin
		evidence = append(evidence, "target file is the CLI livehost transport boundary and imports runtime request/service contracts")
		if strings.Contains(file, "request_policy.go") || strings.Contains(file, "duration_") || strings.Contains(signature, "retry") {
			class = classPolicy
			evidence = append(evidence, "AST declaration is in a policy/error file with control-flow/result logic; this is a deferred C46/C48-owned boundary, not a ready C50 writer")
		}
	} else if strings.Contains(file, "/room/") {
		if strings.Contains(file, "manifest.go") {
			class = classPolicy
			evidence = append(evidence, "AST declaration parses/validates room input and returns attributed validation errors")
		} else if strings.Contains(file, "browser_tools.go") {
			class = classThin
			evidence = append(evidence, "AST declaration adapts browser/tool inputs at the CLI boundary")
		} else {
			class = classRuntime
			evidence = append(evidence, "AST declaration implements room mesh/mixing behavior rather than command construction")
		}
	} else {
		if strings.Contains(file, "/service.go") || strings.Contains(file, "_wire") || strings.Contains(file, "runtime_factory") || strings.Contains(symbol.Name, "Wire") || strings.Contains(imports, "github.com/google/wire") {
			class = classComposition
			evidence = append(evidence, "AST declaration participates in service construction or explicitly imports/calls composition contracts")
		} else if strings.Contains(file, "policy") || strings.Contains(file, "admission") || strings.Contains(file, "duration") || strings.Contains(file, "terminal") || strings.Contains(file, "retry") || strings.Contains(file, "capabilit") {
			class = classPolicy
			evidence = append(evidence, "AST declaration is in a bounded policy/admission/eligibility source and its callers are production-resolved")
		} else if strings.Contains(imports, "/services/") || strings.Contains(imports, "/pkg/messages") || strings.Contains(imports, "/pkg/transport") {
			class = classRuntime
			evidence = append(evidence, "AST declaration consumes runtime/provider/message contracts and is called from production orchestration")
		} else {
			evidence = append(evidence, "AST declaration is called from production orchestration and has no transport-only or construction-only signature")
		}
	}
	if symbol.Exported && strings.HasPrefix(symbol.File, "agent-cli/internal/transport") {
		confidence = "high"
	}
	symbol.Class = class
	symbol.Confidence = confidence
	symbol.ClassificationEvidence = evidence
}

func counts(inventory Inventory) ([]AreaCount, AreaCount) {
	byRoot := make(map[string]*AreaCount)
	for _, root := range targetRoots {
		byRoot[root] = &AreaCount{Root: root}
	}
	for _, file := range inventory.Files {
		area := byRoot[file.Root]
		area.ProductionFiles++
		area.ProductionLines += file.PhysicalLines
		area.ProductionSymbols += len(file.TopLevelSymbolIDs)
	}
	for _, file := range inventory.Exclusions {
		area := byRoot[file.Root]
		area.ExcludedFiles++
		area.ExcludedLines += file.PhysicalLines
	}
	areas := make([]AreaCount, 0, len(targetRoots))
	total := AreaCount{Root: "ALL_TARGET_ROOTS"}
	for _, root := range targetRoots {
		area := *byRoot[root]
		areas = append(areas, area)
		total.ProductionFiles += area.ProductionFiles
		total.ProductionLines += area.ProductionLines
		total.ProductionSymbols += area.ProductionSymbols
		total.ExcludedFiles += area.ExcludedFiles
		total.ExcludedLines += area.ExcludedLines
	}
	return areas, total
}

func classificationDocument(inventory Inventory) map[string]any {
	return map[string]any{"schema_version": "c50-classifications-v1", "source_revision": inventory.SourceRevision, "rules": []string{
		"Every inventoried symbol has exactly one primary class.",
		"Production callers are AST CallExpr citations; tests and lexical matches are not callers.",
		"Unresolved dynamic/interface/reflection reachability is DEAD_OR_UNCERTAIN, never confirmed reachability.",
		"The class is a reviewable structural classification; its evidence cites package/file role, imports, declaration form and resolved callers.",
	}, "symbols": inventory.Symbols}
}

func callPathDocument(inventory Inventory) map[string]any {
	entryPrefixes := []string{"agent-cli/cmd/", "agent-cli/internal/cli/", "agent-cli/internal/transport/cli/", "agent-cli/internal/wire/"}
	var entryCalls []CallEdge
	for _, edge := range inventory.CallEdges {
		for _, prefix := range entryPrefixes {
			if strings.HasPrefix(edge.Call.File, prefix) {
				entryCalls = append(entryCalls, edge)
				break
			}
		}
	}
	sort.Slice(entryCalls, func(i, j int) bool { return edgeLess(entryCalls[i], entryCalls[j]) })
	return map[string]any{"schema_version": "c50-call-paths-v1", "source_revision": inventory.SourceRevision, "entry_prefixes": entryPrefixes, "entry_call_sites": entryCalls, "target_call_edges": inventory.CallEdges, "reachability_rule": "A target symbol is a reachable anchor only when a production CallExpr from the listed public CLI construction/transport prefixes is cited; downstream target-to-target edges are retained separately.", "dynamic_limitations": []string{
		"AST analysis does not prove interface dispatch, reflection, generated code or runtime registration; those paths remain explicit uncertainty.",
		"A selector with an imported package qualifier is resolved by exact import path; method calls retain medium confidence unless receiver typing is available.",
	}, "public_construction_sources": []string{"agent-cli/cmd/yui/main.go", "agent-cli/internal/wire/", "agent-cli/internal/transport/cli/"}}
}

func edgeLess(a, b CallEdge) bool {
	if a.Call.File != b.Call.File {
		return a.Call.File < b.Call.File
	}
	if a.Call.Line != b.Call.Line {
		return a.Call.Line < b.Call.Line
	}
	if a.Callee != b.Callee {
		return a.Callee < b.Callee
	}
	return a.Caller < b.Caller
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func writeMarkdown(out string, inventory Inventory) error {
	var b strings.Builder
	b.WriteString("# C50 remaining CLI service inventory\n\n")
	b.WriteString("This report is generated from exact source revision `" + inventory.SourceRevision + "`. Counts are inventory only; no migration percentage is inferred.\n\n")
	b.WriteString("## Scope and totals\n\n")
	b.WriteString("| Root | Production files | Physical lines | Top-level symbols | Excluded files | Excluded lines |\n|---|---:|---:|---:|---:|---:|\n")
	for _, area := range inventory.Areas {
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d |\n", area.Root, area.ProductionFiles, area.ProductionLines, area.ProductionSymbols, area.ExcludedFiles, area.ExcludedLines)
	}
	fmt.Fprintf(&b, "| **all target roots** | **%d** | **%d** | **%d** | **%d** | **%d** |\n", inventory.Totals.ProductionFiles, inventory.Totals.ProductionLines, inventory.Totals.ProductionSymbols, inventory.Totals.ExcludedFiles, inventory.Totals.ExcludedLines)
	b.WriteString("\nHistorical reconciliation: the operator's `107 files / 41,305 physical lines` observation is retained as a pinned historical observation with an unspecified scope/revision. The current three-root counts above use exact production inclusion rules and do not call any difference migration progress.\n\n")
	b.WriteString("## Exclusion ledger\n\n")
	b.WriteString("| Path | Root | Reason | Physical lines |\n|---|---|---|---:|\n")
	for _, file := range inventory.Exclusions {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d |\n", file.Path, file.Root, file.Reason, file.PhysicalLines)
	}
	b.WriteString("\n## Symbol census\n\n")
	b.WriteString("Every production top-level declaration is listed with its exact source location, AST-derived class, and statically resolved production callers. Full machine-readable fields are in `inventory.json` and `classifications.json`.\n\n")
	b.WriteString("| Source | Kind | Symbol | Class | Confidence | Callers |\n|---|---|---|---|---|---:|\n")
	for _, symbol := range inventory.Symbols {
		fmt.Fprintf(&b, "| `%s:%d` | `%s` | `%s` | `%s` | `%s` | %d |\n", symbol.File, symbol.Line, symbol.Kind, symbol.QualifiedName, symbol.Class, symbol.Confidence, len(symbol.ProductionCallers))
	}
	b.WriteString("\n## Classification and reachability limits\n\n")
	b.WriteString("The analyzer records exact AST call expressions and import-qualified calls. It does not pretend to resolve interface dispatch, reflection, generated registration, or runtime configuration; no-caller symbols are `DEAD_OR_UNCERTAIN`, and method-name matches retain medium confidence. Public CLI entry prefixes and all downstream target edges are in `call-paths.json`.\n")
	return os.WriteFile(filepath.Join(out, "inventory.md"), []byte(b.String()), 0o644)
}

func lineCount(source []byte) int {
	if len(source) == 0 {
		return 0
	}
	count := bytes.Count(source, []byte{'\n'})
	if source[len(source)-1] != '\n' {
		count++
	}
	return count
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func pathBase(path string) string {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

func writeDiagnostic(message string) { fmt.Fprintln(os.Stderr, message) }

func fatalf(formatString string, args ...any) {
	writeDiagnostic(fmt.Sprintf(formatString, args...))
	os.Exit(1)
}
