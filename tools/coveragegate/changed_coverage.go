package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// runChangedCoverage runs coverage only for current workspace packages that
// own changed Go files. The temporary profiles are removed before returning;
// the repository's full coverage target remains responsible for full-suite
// profiles.
func runChangedCoverage(manifestPath, gitBinary, goBinary, repoDir, base, testTimeout string, moduleDirs []string, stdout, stderr io.Writer) error {
	if manifestPath == "" {
		return fmt.Errorf("%w: changed coverage requires --manifest", ErrChangedPackageCoverage)
	}
	if len(moduleDirs) == 0 {
		return fmt.Errorf("%w: changed coverage requires at least one --module-dir", ErrChangedPackageCoverage)
	}
	if strings.TrimSpace(goBinary) == "" {
		goBinary = "go"
	}
	if strings.TrimSpace(testTimeout) == "" {
		return fmt.Errorf("%w: changed coverage requires a non-empty --test-timeout", ErrChangedPackageCoverage)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	selection, err := SelectChangedPackages(context.Background(), gitBinary, goBinary, repoDir, base, moduleDirs)
	if err != nil {
		return err
	}

	if len(selection.UnownedGoFiles) > 0 {
		_, _ = fmt.Fprintln(stdout, "changed coverage ignored Go files without a current workspace package:")
		for _, path := range selection.UnownedGoFiles {
			_, _ = fmt.Fprintf(stdout, "- %s\n", path)
		}
	}
	if len(selection.Packages) == 0 {
		_, _ = fmt.Fprintf(stdout, "changed coverage passed: no changed Go packages relative to %s; no coverage tests ran\n", base)
		return nil
	}

	entries := make(map[string]PackageEntry, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		entries[entry.ImportPath] = entry
	}
	selectedPaths := make([]string, 0, len(selection.Packages))
	missing := make([]string, 0)
	byModule := make(map[string][]WorkspacePackage)
	for _, packageInfo := range selection.Packages {
		selectedPaths = append(selectedPaths, packageInfo.ImportPath)
		entry, registered := entries[packageInfo.ImportPath]
		if !registered {
			missing = append(missing, packageInfo.ImportPath)
			continue
		}
		if entry.HasException {
			_, _ = fmt.Fprintf(stdout, "changed coverage skipped %s: manifest exception: %s\n", packageInfo.ImportPath, entry.Exception)
			continue
		}
		byModule[packageInfo.ModuleDirectory] = append(byModule[packageInfo.ModuleDirectory], packageInfo)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return &FindingsError{Unregistered: missing}
	}
	if len(byModule) == 0 {
		_, _ = fmt.Fprintf(stdout, "changed coverage passed: %d changed package(s) have no numeric coverage floor; no coverage tests ran\n", len(selectedPaths))
		return nil
	}

	profileDirectory, err := os.MkdirTemp("", "coveragegate-changed-")
	if err != nil {
		return fmt.Errorf("%w: create temporary coverage directory: %v", ErrChangedPackageCoverage, err)
	}
	defer os.RemoveAll(profileDirectory)

	modulePaths := make([]string, 0, len(byModule))
	for modulePath := range byModule {
		modulePaths = append(modulePaths, modulePath)
	}
	sort.Strings(modulePaths)
	profilePaths := make([]string, 0, len(modulePaths))
	for index, modulePath := range modulePaths {
		packages := byModule[modulePath]
		sort.Slice(packages, func(i, j int) bool {
			return packages[i].ImportPath < packages[j].ImportPath
		})
		arguments := []string{"test", "-count=1", "-tags=nomicrophone", "-timeout", testTimeout, "-coverprofile", filepath.Join(profileDirectory, fmt.Sprintf("module-%d.out", index))}
		for _, packageInfo := range packages {
			packageArgument, err := packageArgument(packageInfo)
			if err != nil {
				return err
			}
			arguments = append(arguments, packageArgument)
		}
		_, _ = fmt.Fprintf(stdout, "changed coverage testing %d package(s) in %s\n", len(packages), modulePath)
		command := exec.Command(goBinary, arguments...)
		command.Dir = modulePath
		workspaceFile := nearestWorkspaceFile(modulePath)
		if workspaceFile == "" {
			workspaceFile = "off"
		}
		command.Env = setEnvironment(os.Environ(), "GOWORK", workspaceFile)
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("%w: go test for changed packages in %s failed: %v", ErrChangedPackageCoverage, modulePath, err)
		}
		profilePaths = append(profilePaths, filepath.Join(profileDirectory, fmt.Sprintf("module-%d.out", index)))
	}

	measurements, err := ReadProfiles(profilePaths)
	if err != nil {
		return err
	}
	if err := CompareSelected(manifest, selectedPaths, measurements); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "changed coverage passed: %d changed package(s) checked across %d module profile(s)\n", len(selectedPaths), len(profilePaths))
	return nil
}

func packageArgument(packageInfo WorkspacePackage) (string, error) {
	relative, err := filepath.Rel(packageInfo.ModuleDirectory, packageInfo.Directory)
	if err != nil || !pathWithin(packageInfo.ModuleDirectory, packageInfo.Directory) {
		return "", fmt.Errorf("%w: package %q directory %q is not inside module %q", ErrChangedPackageCoverage, packageInfo.ImportPath, packageInfo.Directory, packageInfo.ModuleDirectory)
	}
	if relative == "." {
		return ".", nil
	}
	return "./" + filepath.ToSlash(relative), nil
}
