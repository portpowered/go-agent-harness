package main

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func discoverModule(ctx context.Context, goBinary, dir string, patterns []string, target ...string) (*Module, error) {
	modulePath, err := readModulePath(dir)
	if err != nil {
		return nil, err
	}
	listed, err := listPackages(ctx, goBinary, dir, patterns, target...)
	if err != nil {
		return nil, err
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("module %q selected no packages", dir)
	}
	module := &Module{Dir: dir, Path: modulePath}
	packagesByDir, err := listedPackages(module, listed)
	if err != nil {
		return nil, err
	}
	if err := walkModuleSources(module, modulePath, packagesByDir); err != nil {
		return nil, err
	}
	return finalizeModule(module, packagesByDir)
}

func listedPackages(module *Module, listed []goListPackage) (map[string]*Package, error) {
	packagesByDir := make(map[string]*Package)
	for _, listedPackage := range listed {
		packageDir := listedPackage.Dir
		if !filepath.IsAbs(packageDir) {
			packageDir = filepath.Join(module.Dir, packageDir)
		}
		packageDir = canonicalPath(filepath.Clean(packageDir))
		if !pathWithin(module.Dir, packageDir) {
			return nil, fmt.Errorf("package %q directory %q (go list %q) is outside module %q", listedPackage.ImportPath, packageDir, listedPackage.Dir, module.Dir)
		}
		if previous := packagesByDir[packageDir]; previous != nil && previous.ImportPath != listedPackage.ImportPath {
			return nil, fmt.Errorf("module %q has multiple package import paths for %q: %q and %q", module.Dir, packageDir, previous.ImportPath, listedPackage.ImportPath)
		}
		packagesByDir[packageDir] = &Package{ImportPath: listedPackage.ImportPath, Dir: packageDir, Module: module, TypeLoadable: true}
	}
	return packagesByDir, nil
}

func walkModuleSources(module *Module, modulePath string, packagesByDir map[string]*Package) error {
	err := filepath.WalkDir(module.Dir, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return skipInventoryDirectory(entry.Name())
		}
		if filepath.Ext(name) != ".go" {
			return nil
		}
		return inventorySource(module, modulePath, name, packagesByDir, true)
	})
	if err != nil {
		return fmt.Errorf("inventory module %q: %w", module.Dir, err)
	}
	return nil
}

func skipInventoryDirectory(name string) error {
	if name == "vendor" || name == ".git" || name == "testdata" {
		return filepath.SkipDir
	}
	return nil
}

func inventorySource(module *Module, modulePath, name string, packagesByDir map[string]*Package, selectedOnly bool) error {
	packageDir := filepath.Dir(name)
	pkg := packagesByDir[packageDir]
	if pkg == nil {
		if selectedOnly {
			// go list is the source of truth for an explicitly selected pattern.
			// Do not re-add every other directory while walking the module: doing
			// so makes -pattern/-scope silently ineffective and lets an unrelated
			// package fail a focused gate. Inactive files belonging to a selected
			// package are still visited because the package directory itself is
			// already present in packagesByDir.
			return nil
		}
		var err error
		pkg, err = derivedPackage(module, modulePath, packageDir)
		if err != nil {
			return err
		}
		packagesByDir[packageDir] = pkg
	}
	rel, err := filepath.Rel(module.Dir, name)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	pkg.Files = append(pkg.Files, &SourceFile{Path: name, RelPath: filepath.ToSlash(rel), AST: parsed, Fset: fset, Test: strings.HasSuffix(name, "_test.go"), Generated: hasGeneratedHeader(name)})
	return nil
}

func derivedPackage(module *Module, modulePath, packageDir string) (*Package, error) {
	rel, err := filepath.Rel(module.Dir, packageDir)
	if err != nil {
		return nil, err
	}
	importPath := modulePath
	if rel != "." {
		importPath += "/" + filepath.ToSlash(rel)
	}
	// Snapshot inventories are used by baseline bootstrap. Keep their package
	// paths loadable so type-aware architecture rules (especially exported API
	// leaks) are checked against the exact merge-base source instead of being
	// silently skipped when the baseline file did not exist yet.
	return &Package{ImportPath: importPath, Dir: packageDir, Module: module, TypeLoadable: true}, nil
}

// markSnapshotTypeLoadability keeps inactive platform/tag-only directories in
// the physical inventory while excluding them from the optional historical
// type load. packages.Load reports a hard error for a package with no files
// matching the host build context; that error must not hide otherwise useful
// type-aware checks for the rest of the merge-base snapshot.
func markSnapshotTypeLoadability(packagesByDir map[string]*Package) {
	for _, pkg := range packagesByDir {
		if _, err := build.Default.ImportDir(pkg.Dir, 0); err != nil {
			var noGo *build.NoGoError
			if errors.As(err, &noGo) {
				pkg.TypeLoadable = false
			}
		}
	}
}

func finalizeModule(module *Module, packagesByDir map[string]*Package) (*Module, error) {
	module.Packages = make([]*Package, 0, len(packagesByDir))
	for _, pkg := range packagesByDir {
		if len(pkg.Files) == 0 {
			continue
		}
		sort.Slice(pkg.Files, func(i, j int) bool { return pkg.Files[i].RelPath < pkg.Files[j].RelPath })
		module.Packages = append(module.Packages, pkg)
	}
	sort.Slice(module.Packages, func(i, j int) bool { return module.Packages[i].ImportPath < module.Packages[j].ImportPath })
	if len(module.Packages) == 0 {
		return nil, fmt.Errorf("module %q has no Go source files in selected packages", module.Dir)
	}
	return module, nil
}
