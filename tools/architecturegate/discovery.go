package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Module struct {
	Dir      string
	Path     string
	Packages []*Package
}

type Package struct {
	ImportPath   string
	Dir          string
	Module       *Module
	Files        []*SourceFile
	Types        *packages.Package
	TypeLoadable bool
}

type SourceFile struct {
	Path      string
	RelPath   string
	AST       *ast.File
	Fset      *token.FileSet
	Test      bool
	Generated bool
}

type goListPackage struct {
	ImportPath string
	Dir        string
	Name       string
	Module     *struct {
		Path string
	}
	Error *struct {
		Err string
	}
}

func discoverModules(ctx context.Context, goBinary, repoRoot string, dirs, patterns []string) ([]*Module, error) {
	return discoverModulesForTarget(ctx, goBinary, repoRoot, dirs, patterns, "", "")
}

func discoverModulesForTarget(ctx context.Context, goBinary, repoRoot string, dirs, patterns []string, goos, goarch string) ([]*Module, error) {
	modules := make([]*Module, 0, len(dirs))
	seen := make(map[string]struct{}, len(dirs))
	for _, rawDir := range dirs {
		resolved, err := resolveRepoPath(rawDir, repoRoot)
		if err != nil {
			return nil, fmt.Errorf("module %q: %w", rawDir, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("inspect module %q: %w", rawDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("module %q is not a directory", rawDir)
		}
		resolved = canonicalPath(resolved)
		if _, ok := seen[resolved]; ok {
			return nil, fmt.Errorf("module directory %q was specified more than once", rawDir)
		}
		seen[resolved] = struct{}{}
		module, err := discoverModule(ctx, goBinary, resolved, patterns, goos, goarch)
		if err != nil {
			return nil, err
		}
		modules = append(modules, module)
	}
	return modules, nil
}

func canonicalPath(name string) string {
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(name)
}

func readModulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read module file in %q: %w", dir, err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read module file in %q: %w", dir, err)
	}
	return "", fmt.Errorf("module file in %q has no module directive", dir)
}

func listPackages(ctx context.Context, goBinary, dir string, patterns []string, target ...string) ([]goListPackage, error) {
	if strings.TrimSpace(goBinary) == "" {
		goBinary = "go"
	}
	args := []string{"list", "-json", "-e"}
	args = append(args, patterns...)
	command := exec.CommandContext(ctx, goBinary, args...)
	command.Dir = dir
	command.Env = setTypeLoadEnvironment(os.Environ(), targetValue(target, 0), targetValue(target, 1))
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("go list in %q failed: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	var packages []goListPackage
	for {
		var listed goListPackage
		err := decoder.Decode(&listed)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list in %q: %w", dir, err)
		}
		if listed.Error != nil {
			return nil, fmt.Errorf("package %q in %q failed to load: %s", listed.ImportPath, dir, listed.Error.Err)
		}
		if listed.ImportPath == "" || listed.Dir == "" {
			return nil, fmt.Errorf("go list in %q returned package without import path or directory", dir)
		}
		packages = append(packages, listed)
	}
	return packages, nil
}

func targetValue(target []string, index int) string {
	if index < 0 || index >= len(target) {
		return ""
	}
	return target[index]
}

func setEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, prefix+value)
}

func loadTypes(ctx context.Context, modules []*Module, goos, goarch string) error {
	for _, module := range modules {
		if err := loadModuleTypes(ctx, module, goos, goarch); err != nil {
			return err
		}
	}
	return nil
}

func loadModuleTypes(ctx context.Context, module *Module, goos, goarch string) error {
	patterns := typeLoadPatterns(module)
	if len(patterns) == 0 {
		return nil
	}
	cfg := typeLoadConfig(ctx, module, goos, goarch)
	loaded, err := packages.Load(cfg, patterns...)
	if err != nil {
		return fmt.Errorf("load types for module %q: %w", module.Dir, err)
	}
	byPath, err := typePackagesByPath(loaded)
	if err != nil {
		return err
	}
	for _, pkg := range module.Packages {
		pkg.Types = byPath[pkg.ImportPath]
		if pkg.TypeLoadable && pkg.Types == nil {
			return fmt.Errorf("type loading returned no package for %s", pkg.ImportPath)
		}
	}
	return nil
}

func typeLoadPatterns(module *Module) []string {
	patterns := make([]string, 0, len(module.Packages))
	for _, pkg := range module.Packages {
		if pkg.TypeLoadable {
			patterns = append(patterns, pkg.ImportPath)
		}
	}
	return patterns
}

func typeLoadConfig(ctx context.Context, module *Module, goos, goarch string) *packages.Config {
	cfg := &packages.Config{
		Context: ctx,
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule,
		Dir:     module.Dir,
		Env:     setTypeLoadEnvironment(os.Environ(), goos, goarch),
		Tests:   true,
	}
	return cfg
}

func setTypeLoadEnvironment(environment []string, goos, goarch string) []string {
	environment = setEnv(environment, "GOWORK", "off")
	if goos != "" {
		environment = setEnv(environment, "GOOS", goos)
	}
	if goarch != "" {
		environment = setEnv(environment, "GOARCH", goarch)
	}
	return environment
}

func typePackagesByPath(loaded []*packages.Package) (map[string]*packages.Package, error) {
	byPath := make(map[string]*packages.Package, len(loaded))
	for _, loadedPackage := range loaded {
		if len(loadedPackage.Errors) > 0 {
			return nil, fmt.Errorf("type loading failed for %s: %s", loadedPackage.PkgPath, formatPackageErrors(loadedPackage.Errors))
		}
		// Tests:true returns synthetic test variants with the same package
		// path. Keep the ordinary package for public-surface checks; its
		// exported API is the one consumers can actually import.
		if existing := byPath[loadedPackage.PkgPath]; existing == nil || loadedPackage.ForTest == "" {
			byPath[loadedPackage.PkgPath] = loadedPackage
		}
	}
	return byPath, nil
}

func formatPackageErrors(errorsList []packages.Error) string {
	parts := make([]string, len(errorsList))
	for index, packageError := range errorsList {
		parts[index] = packageError.Error()
	}
	return strings.Join(parts, "; ")
}
