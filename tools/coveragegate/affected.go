package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ErrAffectedScope reports a failure to compute the changed-package test scope.
var ErrAffectedScope = errors.New("affected-package scope failed")

// isFullScopePath reports whether a repository-relative path can alter how
// every package is built or tested (workspace files, the Make and shard
// orchestration, and the coverage gate itself), so a changed-package run
// must fall back to the full suite.
func isFullScopePath(path string) bool {
	switch path {
	case "go.work", "go.work.sum", "Makefile", "scripts/prepush.sh", "scripts/go-test-shards.sh", "scripts/run-bounded.sh":
		return true
	}
	return strings.HasPrefix(path, "tools/coveragegate/")
}

// coverageManifestDirectory holds one floor fragment per package, mirroring
// the package directory layout below the repository root.
const coverageManifestDirectory = "coverage-manifest/"

// AffectedModule is one Go module whose packages take part in scope
// selection. Standalone modules are listed with GOWORK=off.
type AffectedModule struct {
	Directory  string
	Standalone bool
}

// AffectedScope is the changed-package test and coverage-floor scope.
// FullReason is set when the change cannot be scoped safely; Tests maps a
// repository-relative module directory to the ./relative test packages to
// run; Check lists the import paths whose coverage floors are exact for
// the partial run and must be enforced.
type AffectedScope struct {
	FullReason string
	Changed    []string
	Tests      map[string][]string
	Check      []string
}

// affectedPackage is one workspace package with its dependency sets under
// the coverage build configuration.
type affectedPackage struct {
	importPath string
	directory  string
	module     string
	relative   string
	deps       map[string]struct{}
	testDeps   map[string]struct{}
	hasTests   bool
	// standalone packages (e.g. the embedding consumer) carry no floors.
	standalone bool
}

type affectedListing struct {
	ImportPath   string
	Dir          string
	ForTest      string
	Deps         []string
	TestGoFiles  []string
	XTestGoFiles []string
}

// SelectAffectedScope maps the changes since base to the reverse dependency
// closure of the changed packages. A test package is selected when its test
// binary links a changed package; a package's floor is checked when it is
// changed or imports a changed package, because every test that can cover
// such a package links the changed package and is therefore selected.
func SelectAffectedScope(ctx context.Context, gitBinary, goBinary, repoDir, base string, tags string, modules []AffectedModule) (AffectedScope, error) {
	repoRoot, err := filepath.Abs(repoDir)
	if err != nil {
		return AffectedScope{}, fmt.Errorf("%w: resolve repository %q: %w", ErrAffectedScope, repoDir, err)
	}
	repoRoot = canonicalPath(repoRoot)
	changes, err := collectGitChanges(ctx, gitBinary, repoRoot, base)
	if err != nil {
		return AffectedScope{}, err
	}
	paths := changedPathSet(changes)
	if reason := fullScopeReason(paths, repoRoot, modules); reason != "" {
		return AffectedScope{FullReason: reason}, nil
	}
	packages, err := listAffectedPackages(ctx, goBinary, repoRoot, tags, modules)
	if err != nil {
		return AffectedScope{}, err
	}
	changed, reason := changedPackagesFor(paths, repoRoot, modules, packages)
	if reason != "" {
		return AffectedScope{FullReason: reason}, nil
	}
	return closeOverChanged(changed, packages), nil
}

func changedPathSet(changes []fileChange) []string {
	set := make(map[string]struct{})
	for _, change := range changes {
		for _, path := range []string{change.Path, change.OldPath} {
			if path != "" {
				set[filepath.ToSlash(path)] = struct{}{}
			}
		}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// fullScopeReason returns why the change set needs the full suite, or "".
func fullScopeReason(paths []string, repoRoot string, modules []AffectedModule) string {
	moduleFiles := make(map[string]struct{})
	for _, module := range modules {
		relative := repoRelative(repoRoot, module.Directory)
		for _, name := range []string{"go.mod", "go.sum"} {
			moduleFiles[strings.TrimPrefix(relative+"/"+name, "./")] = struct{}{}
		}
	}
	for _, path := range paths {
		if _, ok := moduleFiles[path]; ok || isFullScopePath(path) {
			return "changed " + path
		}
	}
	return ""
}

func repoRelative(repoRoot, directory string) string {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return filepath.ToSlash(directory)
	}
	relative, err := filepath.Rel(repoRoot, canonicalPath(absolute))
	if err != nil {
		return filepath.ToSlash(directory)
	}
	return filepath.ToSlash(relative)
}

// listAffectedPackages lists every package of every module with the
// dependencies of the package and of its test binary.
func listAffectedPackages(ctx context.Context, goBinary, repoRoot, tags string, modules []AffectedModule) (map[string]*affectedPackage, error) {
	packages := make(map[string]*affectedPackage)
	for _, module := range modules {
		moduleDir, err := filepath.Abs(module.Directory)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve module %q: %w", ErrAffectedScope, module.Directory, err)
		}
		moduleDir = canonicalPath(moduleDir)
		listing, err := goListTestDeps(ctx, goBinary, moduleDir, tags, module.Standalone)
		if err != nil {
			return nil, err
		}
		moduleRelative := repoRelative(repoRoot, moduleDir)
		if err := decodeAffectedListing(listing, moduleDir, moduleRelative, packages); err != nil {
			return nil, err
		}
		for _, packageInfo := range packages {
			if packageInfo.module == moduleRelative {
				packageInfo.standalone = module.Standalone
			}
		}
	}
	return packages, nil
}

func goListTestDeps(ctx context.Context, goBinary, moduleDir, tags string, standalone bool) (string, error) {
	arguments := []string{"list", "-e", "-test", "-json=ImportPath,Dir,ForTest,Deps,TestGoFiles,XTestGoFiles"}
	if strings.TrimSpace(tags) != "" {
		arguments = append(arguments, "-tags="+tags)
	}
	arguments = append(arguments, "./...")
	command := exec.CommandContext(ctx, goBinary, arguments...)
	command.Dir = moduleDir
	workspaceFile := "off"
	if !standalone {
		if nearest := nearestWorkspaceFile(moduleDir); nearest != "" {
			workspaceFile = nearest
		}
	}
	command.Env = setEnvironment(os.Environ(), "GOWORK", workspaceFile)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", externalCommandError(ErrAffectedScope, "go list", err, stdout.String(), stderr.String(), moduleDir)
	}
	return stdout.String(), nil
}

func decodeAffectedListing(listing, moduleDir, moduleRelative string, packages map[string]*affectedPackage) error {
	testMains := make(map[string][]string)
	decoder := json.NewDecoder(strings.NewReader(listing))
	for {
		var listed affectedListing
		err := decoder.Decode(&listed)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: decode go list output in %q: %w", ErrAffectedScope, moduleDir, err)
		}
		switch {
		case strings.HasSuffix(listed.ImportPath, ".test") && listed.ForTest == "":
			testMains[strings.TrimSuffix(listed.ImportPath, ".test")] = listed.Deps
		case strings.Contains(listed.ImportPath, " "):
			// A package variant recompiled for a test binary.
		default:
			if err := addAffectedPackage(listed, moduleDir, moduleRelative, packages); err != nil {
				return err
			}
		}
	}
	for importPath, deps := range testMains {
		if packageInfo, ok := packages[importPath]; ok {
			packageInfo.testDeps = stripTestVariants(deps)
			packageInfo.testDeps[importPath] = struct{}{}
		}
	}
	return nil
}

func addAffectedPackage(listed affectedListing, moduleDir, moduleRelative string, packages map[string]*affectedPackage) error {
	directory := canonicalPath(filepath.Clean(listed.Dir))
	if listed.ImportPath == "" || !pathWithin(moduleDir, directory) {
		return fmt.Errorf("%w: package %q directory %q is outside module %q", ErrAffectedScope, listed.ImportPath, listed.Dir, moduleDir)
	}
	if previous, duplicate := packages[listed.ImportPath]; duplicate {
		return fmt.Errorf("%w: package %q was listed by both %q and %q", ErrAffectedScope, listed.ImportPath, previous.module, moduleRelative)
	}
	relative, err := filepath.Rel(moduleDir, directory)
	if err != nil {
		return fmt.Errorf("%w: package %q directory: %w", ErrAffectedScope, listed.ImportPath, err)
	}
	packageArgument := "./" + filepath.ToSlash(relative)
	if relative == "." {
		packageArgument = "."
	}
	packages[listed.ImportPath] = &affectedPackage{
		importPath: listed.ImportPath,
		directory:  directory,
		module:     moduleRelative,
		relative:   packageArgument,
		deps:       stripTestVariants(listed.Deps),
		testDeps:   map[string]struct{}{},
		hasTests:   len(listed.TestGoFiles)+len(listed.XTestGoFiles) > 0,
	}
	return nil
}

// stripTestVariants turns "p [q.test]" dependency entries into "p".
func stripTestVariants(deps []string) map[string]struct{} {
	set := make(map[string]struct{}, len(deps))
	for _, dep := range deps {
		if index := strings.IndexByte(dep, ' '); index >= 0 {
			dep = dep[:index]
		}
		set[dep] = struct{}{}
	}
	return set
}

// changedPackagesFor maps every changed path inside a module to the package
// owning its nearest enclosing package directory. Changed coverage floors
// map to the package they describe. A file inside a module but outside
// every package (other than documentation) cannot be scoped.
func changedPackagesFor(paths []string, repoRoot string, modules []AffectedModule, packages map[string]*affectedPackage) (map[string]struct{}, string) {
	byDirectory := make(map[string]string, len(packages))
	for _, packageInfo := range packages {
		byDirectory[packageInfo.directory] = packageInfo.importPath
	}
	moduleDirs := make([]string, 0, len(modules))
	for _, module := range modules {
		if absolute, err := filepath.Abs(module.Directory); err == nil {
			moduleDirs = append(moduleDirs, canonicalPath(absolute))
		}
	}
	changed := make(map[string]struct{})
	for _, path := range paths {
		target := path
		if strings.HasPrefix(path, coverageManifestDirectory) {
			target = strings.TrimSuffix(strings.TrimPrefix(path, coverageManifestDirectory), ".json") + "/floor"
		}
		absolute := filepath.Join(repoRoot, filepath.FromSlash(target))
		moduleDir := enclosingModule(absolute, moduleDirs)
		if moduleDir == "" {
			continue
		}
		importPath := enclosingPackage(filepath.Dir(absolute), moduleDir, byDirectory)
		switch {
		case importPath != "":
			changed[importPath] = struct{}{}
		case strings.HasSuffix(path, ".md") || strings.HasPrefix(path, coverageManifestDirectory):
		default:
			return nil, "changed " + path + " outside every package"
		}
	}
	return changed, ""
}

func enclosingModule(path string, moduleDirs []string) string {
	best := ""
	for _, moduleDir := range moduleDirs {
		if pathWithin(moduleDir, path) && len(moduleDir) > len(best) {
			best = moduleDir
		}
	}
	return best
}

func enclosingPackage(directory, moduleDir string, byDirectory map[string]string) string {
	for pathWithin(moduleDir, directory) {
		if importPath, ok := byDirectory[directory]; ok {
			return importPath
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return ""
}

func closeOverChanged(changed map[string]struct{}, packages map[string]*affectedPackage) AffectedScope {
	scope := AffectedScope{Tests: make(map[string][]string)}
	for importPath := range changed {
		scope.Changed = append(scope.Changed, importPath)
	}
	sort.Strings(scope.Changed)
	for _, packageInfo := range packages {
		if packageInfo.hasTests && intersects(packageInfo.testDeps, changed) {
			scope.Tests[packageInfo.module] = append(scope.Tests[packageInfo.module], packageInfo.relative)
		}
		if packageInfo.standalone {
			continue
		}
		if _, direct := changed[packageInfo.importPath]; direct || intersects(packageInfo.deps, changed) {
			scope.Check = append(scope.Check, packageInfo.importPath)
		}
	}
	for module := range scope.Tests {
		sort.Strings(scope.Tests[module])
	}
	sort.Strings(scope.Check)
	return scope
}

func intersects(set, changed map[string]struct{}) bool {
	for importPath := range changed {
		if _, ok := set[importPath]; ok {
			return true
		}
	}
	return false
}

// WriteAffectedScope prints the scope as line records: "scope full REASON"
// or "scope changed", then "changed IMPORT", "test MODULE PACKAGE" and
// "check IMPORT" lines in sorted order.
func WriteAffectedScope(writer io.Writer, scope AffectedScope) error {
	var builder strings.Builder
	if scope.FullReason != "" {
		fmt.Fprintf(&builder, "scope full %s\n", scope.FullReason)
		_, err := io.WriteString(writer, builder.String())
		return err
	}
	builder.WriteString("scope changed\n")
	for _, importPath := range scope.Changed {
		fmt.Fprintf(&builder, "changed %s\n", importPath)
	}
	modules := make([]string, 0, len(scope.Tests))
	for module := range scope.Tests {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	for _, module := range modules {
		for _, packageArgument := range scope.Tests[module] {
			fmt.Fprintf(&builder, "test %s %s\n", module, packageArgument)
		}
	}
	for _, importPath := range scope.Check {
		fmt.Fprintf(&builder, "check %s\n", importPath)
	}
	_, err := io.WriteString(writer, builder.String())
	return err
}
