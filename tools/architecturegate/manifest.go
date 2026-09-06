package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	policyVersion = 1

	defaultPackageFileLimit        = 15
	defaultProductionFileLineLimit = 400
	defaultTestFileLineLimit       = 600
	defaultFunctionLineLimit       = 80
	defaultTestFunctionLineLimit   = 120
	defaultFunctionStatementLimit  = 50
	defaultTestStatementLimit      = 80
	defaultCognitiveLimit          = 15
	defaultTestCognitiveLimit      = 20
	defaultCyclomaticLimit         = 15
	defaultTestCyclomaticLimit     = 20
)

// Policy is the reviewed architecture manifest. Patterns are repository
// relative for directories and Go import paths for import rules.
type Policy struct {
	Version                int                `json:"version"`
	ModuleDirs             []string           `json:"module_dirs"`
	Patterns               []string           `json:"patterns"`
	Baseline               string             `json:"baseline"`
	ServiceRoots           []string           `json:"service_roots"`
	CompositionRoots       []string           `json:"composition_roots"`
	CompositionRegistry    []CompositionEntry `json:"composition_registry"`
	ModuleRules            []ModuleRule       `json:"module_rules"`
	ReusableModules        []string           `json:"reusable_modules"`
	CLIModules             []string           `json:"cli_modules"`
	ForbiddenImports       []ImportRule       `json:"forbidden_imports"`
	ForbiddenRootImports   []string           `json:"forbidden_root_imports"`
	GeneratedFiles         []GeneratedRule    `json:"generated_files"`
	GlobalExceptions       []GlobalException  `json:"global_exceptions"`
	GlobalStateScopes      []string           `json:"global_state_scopes"`
	RootFunctionExceptions []GlobalException  `json:"root_function_exceptions"`
	Limits                 Limits             `json:"limits"`
}

type ImportRule struct {
	From    []string `json:"from"`
	Imports []string `json:"imports"`
	Reason  string   `json:"reason"`
}

type GeneratedRule struct {
	Module    string `json:"module,omitempty"`
	Pattern   string `json:"pattern"`
	Generator string `json:"generator"`
	Header    string `json:"header,omitempty"`
}

// ModuleRule narrows the top-level directory families admitted by a module.
// It is intentionally explicit for extracted runtimes: a reusable runtime
// should expose services and composition, rather than silently growing a
// second public implementation tree.
type ModuleRule struct {
	Module          string   `json:"module"`
	AllowedTopLevel []string `json:"allowed_top_level"`
}

type GlobalException struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// CompositionEntry records one explicit host or test source that is allowed
// to assemble service wires. Package entries apply to the whole package (for
// example an external consumer module); entries with File are restricted to a
// single test source. This registry keeps composition authority reviewable
// without granting every package a wildcard wire exemption.
type CompositionEntry struct {
	Module  string `json:"module"`
	Package string `json:"package"`
	File    string `json:"file,omitempty"`
	Reason  string `json:"reason"`
}

type Limits struct {
	PackageFiles        int `json:"package_files"`
	ProductionFileLines int `json:"production_file_lines"`
	TestFileLines       int `json:"test_file_lines"`
	FunctionLines       int `json:"function_lines"`
	TestFunctionLines   int `json:"test_function_lines"`
	FunctionStatements  int `json:"function_statements"`
	TestStatements      int `json:"test_function_statements"`
	Cognitive           int `json:"cognitive_complexity"`
	TestCognitive       int `json:"test_cognitive_complexity"`
	Cyclomatic          int `json:"cyclomatic_complexity"`
	TestCyclomatic      int `json:"test_cyclomatic_complexity"`
}

func defaultLimits() Limits {
	return Limits{
		PackageFiles: defaultPackageFileLimit, ProductionFileLines: defaultProductionFileLineLimit, TestFileLines: defaultTestFileLineLimit,
		FunctionLines: defaultFunctionLineLimit, TestFunctionLines: defaultTestFunctionLineLimit,
		FunctionStatements: defaultFunctionStatementLimit, TestStatements: defaultTestStatementLimit,
		Cognitive: defaultCognitiveLimit, TestCognitive: defaultTestCognitiveLimit, Cyclomatic: defaultCyclomaticLimit, TestCyclomatic: defaultTestCyclomaticLimit,
	}
}

func (p *Policy) applyDefaults() {
	defaults := defaultLimits()
	if p.Version == 0 {
		p.Version = policyVersion
	}
	if len(p.Patterns) == 0 {
		p.Patterns = []string{"./..."}
	}
	if p.Limits.PackageFiles == 0 {
		p.Limits.PackageFiles = defaults.PackageFiles
	}
	if p.Limits.ProductionFileLines == 0 {
		p.Limits.ProductionFileLines = defaults.ProductionFileLines
	}
	if p.Limits.TestFileLines == 0 {
		p.Limits.TestFileLines = defaults.TestFileLines
	}
	if p.Limits.FunctionLines == 0 {
		p.Limits.FunctionLines = defaults.FunctionLines
	}
	if p.Limits.TestFunctionLines == 0 {
		p.Limits.TestFunctionLines = defaults.TestFunctionLines
	}
	if p.Limits.FunctionStatements == 0 {
		p.Limits.FunctionStatements = defaults.FunctionStatements
	}
	if p.Limits.TestStatements == 0 {
		p.Limits.TestStatements = defaults.TestStatements
	}
	if p.Limits.Cognitive == 0 {
		p.Limits.Cognitive = defaults.Cognitive
	}
	if p.Limits.TestCognitive == 0 {
		p.Limits.TestCognitive = defaults.TestCognitive
	}
	if p.Limits.Cyclomatic == 0 {
		p.Limits.Cyclomatic = defaults.Cyclomatic
	}
	if p.Limits.TestCyclomatic == 0 {
		p.Limits.TestCyclomatic = defaults.TestCyclomatic
	}
}

func loadPolicy(path, repoRoot string) (Policy, error) {
	if strings.TrimSpace(path) == "" {
		return Policy{Version: policyVersion, Limits: defaultLimits()}, nil
	}
	abs, err := resolveRepoPath(path, repoRoot)
	if err != nil {
		return Policy{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Policy{}, fmt.Errorf("read architecture manifest %q: %w", path, err)
	}
	var policy Policy
	if err := json.Unmarshal(data, &policy); err != nil {
		return Policy{}, fmt.Errorf("decode architecture manifest %q: %w", path, err)
	}
	if policy.Version != policyVersion {
		return Policy{}, fmt.Errorf("architecture manifest %q has version %d; expected %d", path, policy.Version, policyVersion)
	}
	policy.applyDefaults()
	if err := validatePolicy(policy); err != nil {
		return Policy{}, fmt.Errorf("invalid architecture manifest %q: %w", path, err)
	}
	return policy, nil
}

func validatePolicy(policy Policy) error {
	if err := validateModuleDirs(policy.ModuleDirs); err != nil {
		return err
	}
	if err := validateDirectoryRoots(policy.ServiceRoots, policy.CompositionRoots); err != nil {
		return err
	}
	if err := validateCompositionRegistry(policy.CompositionRegistry); err != nil {
		return err
	}
	if err := validateModuleRules(policy.ModuleRules); err != nil {
		return err
	}
	if err := validateImportRules(policy.ForbiddenImports); err != nil {
		return err
	}
	if err := validateGeneratedRules(policy.GeneratedFiles); err != nil {
		return err
	}
	if err := validateGlobalExceptions(policy.GlobalExceptions); err != nil {
		return err
	}
	if err := validateGlobalExceptions(policy.RootFunctionExceptions); err != nil {
		return err
	}
	return validateLimits(policy.Limits)
}

func validateModuleDirs(dirs []string) error {
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			return errors.New("module_dirs cannot contain an empty path")
		}
	}
	return nil
}

func validateDirectoryRoots(serviceRoots, compositionRoots []string) error {
	for _, pattern := range append(append([]string{}, serviceRoots...), compositionRoots...) {
		if strings.TrimSpace(pattern) == "" {
			return errors.New("directory patterns cannot be empty")
		}
	}
	for _, pattern := range compositionRoots {
		if strings.ContainsAny(pattern, "*?") {
			return fmt.Errorf("composition_roots entry %q must be an explicit package path; wildcards can bless arbitrary wire packages", pattern)
		}
	}
	return nil
}

func validateCompositionRegistry(entries []CompositionEntry) error {
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		if err := validateCompositionEntry(index, entry, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateCompositionEntry(index int, entry CompositionEntry, seen map[string]struct{}) error {
	if strings.TrimSpace(entry.Module) == "" || strings.TrimSpace(entry.Package) == "" || strings.TrimSpace(entry.Reason) == "" {
		return fmt.Errorf("composition_registry entry %d requires module, package, and reason", index)
	}
	if strings.ContainsAny(entry.Module, "*?") || strings.ContainsAny(entry.Package, "*?") {
		return fmt.Errorf("composition_registry entry %d must use exact module and package paths", index)
	}
	file, err := validateCompositionFile(index, entry.File)
	if err != nil {
		return err
	}
	key := entry.Module + "\x00" + entry.Package + "\x00" + file
	if _, exists := seen[key]; exists {
		return fmt.Errorf("composition_registry entry %d duplicates %q", index, key)
	}
	seen[key] = struct{}{}
	return nil
}

func validateCompositionFile(index int, raw string) (string, error) {
	file := filepath.ToSlash(strings.TrimSpace(raw))
	if file == "" {
		return "", nil
	}
	cleanFile := filepath.ToSlash(filepath.Clean(file))
	if filepath.IsAbs(file) || cleanFile != file || cleanFile == ".." || strings.HasPrefix(cleanFile, "../") {
		return "", fmt.Errorf("composition_registry entry %d file %q must stay within its module", index, raw)
	}
	if strings.ContainsAny(file, "*?") || !strings.HasSuffix(file, "_test.go") {
		return "", fmt.Errorf("composition_registry entry %d file %q must be an exact _test.go path", index, raw)
	}
	return file, nil
}

// registeredCompositionPackage reports whether a complete package is an
// explicitly reviewed composition boundary. File-scoped entries deliberately
// do not elevate the package: they only authorize the named test source.
func registeredCompositionPackage(pkg *Package, module *Module, policy Policy) bool {
	for _, entry := range policy.CompositionRegistry {
		if compositionEntryMatches(entry, pkg, module) && entry.File == "" {
			return true
		}
	}
	return false
}

// registeredCompositionSource reports whether one source file is an explicit
// composition boundary. The caller still applies test-private import rules,
// so this registry grants assembly authority without permitting private
// implementation imports.
func registeredCompositionSource(pkg *Package, module *Module, source *SourceFile, policy Policy) bool {
	for _, entry := range policy.CompositionRegistry {
		if !compositionEntryMatches(entry, pkg, module) {
			continue
		}
		if entry.File == "" || filepath.ToSlash(entry.File) == source.RelPath {
			return true
		}
	}
	return false
}

func compositionEntryMatches(entry CompositionEntry, pkg *Package, module *Module) bool {
	if module == nil || pkg == nil {
		return false
	}
	return exactModuleMatch(entry.Module, module) && entry.Package == pkg.ImportPath
}

func exactModuleMatch(pattern string, module *Module) bool {
	return pattern == module.Path || pattern == module.Dir || pattern == filepath.Base(module.Dir)
}

func validateModuleRules(rules []ModuleRule) error {
	for _, rule := range rules {
		if strings.TrimSpace(rule.Module) == "" {
			return errors.New("module_rules entries require module")
		}
		if len(rule.AllowedTopLevel) == 0 {
			return fmt.Errorf("module_rules entry %q requires allowed_top_level", rule.Module)
		}
		for _, topLevel := range rule.AllowedTopLevel {
			if strings.TrimSpace(topLevel) == "" || strings.ContainsAny(topLevel, "/\\") {
				return fmt.Errorf("module_rules entry %q has invalid top-level directory %q", rule.Module, topLevel)
			}
		}
	}
	return nil
}

func validateImportRules(rules []ImportRule) error {
	for _, rule := range rules {
		if len(rule.From) == 0 || len(rule.Imports) == 0 {
			return errors.New("forbidden_imports entries require from and imports")
		}
		if strings.TrimSpace(rule.Reason) == "" {
			return errors.New("forbidden_imports entries require a reason")
		}
	}
	return nil
}

func validateGeneratedRules(rules []GeneratedRule) error {
	for _, rule := range rules {
		if strings.TrimSpace(rule.Pattern) == "" || strings.TrimSpace(rule.Generator) == "" {
			return errors.New("generated_files entries require pattern and generator")
		}
		if strings.TrimSpace(rule.Module) == "" && strings.Contains(rule.Pattern, "**") {
			return fmt.Errorf("generated_files entry %q must name a module when using recursive patterns", rule.Pattern)
		}
	}
	return nil
}

func validateGlobalExceptions(exceptions []GlobalException) error {
	for _, exception := range exceptions {
		if exception.Package == "" || exception.Name == "" || exception.Reason == "" {
			return errors.New("global_exceptions entries require package, name, and reason")
		}
	}
	return nil
}

func validateLimits(limits Limits) error {
	if limits.PackageFiles < 1 || limits.ProductionFileLines < 1 || limits.TestFileLines < 1 || limits.FunctionLines < 1 || limits.TestFunctionLines < 1 || limits.FunctionStatements < 1 || limits.TestStatements < 1 || limits.Cognitive < 1 || limits.TestCognitive < 1 || limits.Cyclomatic < 1 || limits.TestCyclomatic < 1 {
		return errors.New("all limits must be positive")
	}
	return nil
}

func resolveRepoPath(path, repoRoot string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path cannot be empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	abs = filepath.Clean(abs)
	if !pathWithin(repoRoot, abs) {
		return "", fmt.Errorf("path %q escapes repository %q", path, repoRoot)
	}
	return abs, nil
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
