package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrPackageDiscovery      = errors.New("workspace package discovery failed")
	ErrRegistrationMissing   = errors.New("workspace package is missing from the coverage manifest")
	ErrRegistrationStale     = errors.New("coverage manifest contains a package outside the workspace")
	ErrRegistrationDuplicate = errors.New("workspace package was discovered more than once")
)

// RegistrationError reports the set difference between the current workspace
// packages and the hand-maintained coverage manifest.
type RegistrationError struct {
	Missing []string
	Stale   []string
}

func (e *RegistrationError) Error() string {
	var sections []string
	if len(e.Missing) > 0 {
		var b strings.Builder
		b.WriteString("coverage registration found workspace packages missing from coverage-manifest.json (update coverage-manifest.json):")
		for _, packagePath := range e.Missing {
			fmt.Fprintf(&b, "\n- %s", packagePath)
		}
		sections = append(sections, b.String())
	}
	if len(e.Stale) > 0 {
		var b strings.Builder
		b.WriteString("coverage registration found manifest packages outside the current workspace (remove stale entries):")
		for _, packagePath := range e.Stale {
			fmt.Fprintf(&b, "\n- %s", packagePath)
		}
		sections = append(sections, b.String())
	}
	return strings.Join(sections, "\n")
}

func (e *RegistrationError) Unwrap() []error {
	var causes []error
	if len(e.Missing) > 0 {
		causes = append(causes, ErrRegistrationMissing)
	}
	if len(e.Stale) > 0 {
		causes = append(causes, ErrRegistrationStale)
	}
	return causes
}

// DiscoverWorkspacePackages runs go list once per workspace module and
// returns the unique import paths in deterministic order. A module uses the
// nearest go.work file when one contains it, which preserves local workspace
// replacements; isolated modules use module mode with GOWORK=off.
func DiscoverWorkspacePackages(ctx context.Context, goBinary string, moduleDirs []string) ([]string, error) {
	if len(moduleDirs) == 0 {
		return nil, fmt.Errorf("%w: no workspace module directories were provided", ErrPackageDiscovery)
	}
	if strings.TrimSpace(goBinary) == "" {
		goBinary = "go"
	}

	packages := make(map[string]string)
	seenModules := make(map[string]struct{}, len(moduleDirs))
	for _, moduleDir := range moduleDirs {
		moduleDir = strings.TrimSpace(moduleDir)
		if moduleDir == "" {
			return nil, fmt.Errorf("%w: workspace module directory is empty", ErrPackageDiscovery)
		}
		absoluteModuleDir, err := filepath.Abs(moduleDir)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve workspace module directory %q: %v", ErrPackageDiscovery, moduleDir, err)
		}
		absoluteModuleDir = filepath.Clean(absoluteModuleDir)
		if _, alreadySeen := seenModules[absoluteModuleDir]; alreadySeen {
			return nil, fmt.Errorf("%w: workspace module directory %q was provided more than once", ErrPackageDiscovery, moduleDir)
		}
		seenModules[absoluteModuleDir] = struct{}{}

		info, err := os.Stat(absoluteModuleDir)
		if err != nil {
			return nil, fmt.Errorf("%w: inspect workspace module directory %q: %v", ErrPackageDiscovery, moduleDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: workspace module path %q is not a directory", ErrPackageDiscovery, moduleDir)
		}

		var stdout, stderr strings.Builder
		command := exec.CommandContext(ctx, goBinary, "list", "-f", "{{.ImportPath}}", "./...")
		command.Dir = absoluteModuleDir
		workspaceFile := nearestWorkspaceFile(absoluteModuleDir)
		if workspaceFile == "" {
			workspaceFile = "off"
		}
		command.Env = setEnvironment(os.Environ(), "GOWORK", workspaceFile)
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = strings.TrimSpace(stdout.String())
			}
			if detail != "" {
				return nil, fmt.Errorf("%w: go list in %q failed: %v: %s", ErrPackageDiscovery, moduleDir, err, detail)
			}
			return nil, fmt.Errorf("%w: go list in %q failed: %v", ErrPackageDiscovery, moduleDir, err)
		}

		modulePackages := 0
		for _, rawPath := range strings.Split(stdout.String(), "\n") {
			packagePath := strings.TrimSpace(rawPath)
			if packagePath == "" {
				continue
			}
			modulePackages++
			if previousModule, alreadyDiscovered := packages[packagePath]; alreadyDiscovered {
				return nil, fmt.Errorf("%w: package %q was listed by both %q and %q", ErrRegistrationDuplicate, packagePath, previousModule, moduleDir)
			}
			packages[packagePath] = moduleDir
		}
		if modulePackages == 0 {
			return nil, fmt.Errorf("%w: go list in %q returned no packages", ErrPackageDiscovery, moduleDir)
		}
	}

	paths := make([]string, 0, len(packages))
	for packagePath := range packages {
		paths = append(paths, packagePath)
	}
	sort.Strings(paths)
	return paths, nil
}

// DiscoverPackages is kept as a short alias for callers that only need the
// package list and do not need to distinguish it from workspace discovery.
func DiscoverPackages(ctx context.Context, goBinary string, moduleDirs []string) ([]string, error) {
	return DiscoverWorkspacePackages(ctx, goBinary, moduleDirs)
}

// ValidateRegistration checks that the current workspace and manifest are
// the same closed set. Manifest syntax, ordering, entry shape, and duplicate
// entries are validated before the set comparison.
func ValidateRegistration(manifest Manifest, discovered []string) error {
	if err := validateManifest(manifest); err != nil {
		return err
	}

	registered := make(map[string]struct{}, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		registered[entry.ImportPath] = struct{}{}
	}

	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, rawPath := range discovered {
		packagePath := strings.TrimSpace(rawPath)
		if packagePath == "" {
			return fmt.Errorf("%w: discovered package path is empty", ErrPackageDiscovery)
		}
		if _, duplicate := discoveredSet[packagePath]; duplicate {
			return fmt.Errorf("%w: package %q appears more than once in the discovered workspace", ErrRegistrationDuplicate, packagePath)
		}
		discoveredSet[packagePath] = struct{}{}
	}

	missing := make([]string, 0)
	for packagePath := range discoveredSet {
		if _, ok := registered[packagePath]; !ok {
			missing = append(missing, packagePath)
		}
	}
	stale := make([]string, 0)
	for packagePath := range registered {
		if _, ok := discoveredSet[packagePath]; !ok {
			stale = append(stale, packagePath)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 || len(stale) > 0 {
		return &RegistrationError{Missing: missing, Stale: stale}
	}
	return nil
}

func setEnvironment(environment []string, key, value string) []string {
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

func nearestWorkspaceFile(directory string) string {
	for {
		candidate := filepath.Join(directory, "go.work")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}
