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
	report := &progressReport{writer: stdout}
	run := coverageRun{goBinary: goBinary, base: base, testTimeout: testTimeout, stdout: stdout, stderr: stderr}
	if err := coverSelection(manifest, selection, run, report); err != nil {
		return err
	}
	return report.err
}

// progressReport writes human-readable progress and keeps the first write
// failure so the command can return it after the coverage decision.
type progressReport struct {
	writer io.Writer
	err    error
}

func (r *progressReport) printf(format string, args ...any) {
	if r.err != nil {
		return
	}
	if _, err := fmt.Fprintf(r.writer, format, args...); err != nil {
		r.err = fmt.Errorf("%w: write changed coverage report: %w", ErrChangedPackageCoverage, err)
	}
}

type coverageRun struct {
	goBinary, base, testTimeout string
	stdout, stderr              io.Writer
}

func coverSelection(manifest Manifest, selection ChangedPackageSelection, run coverageRun, report *progressReport) error {
	if len(selection.UnownedGoFiles) > 0 {
		report.printf("changed coverage ignored Go files without a current workspace package:\n")
		for _, path := range selection.UnownedGoFiles {
			report.printf("- %s\n", path)
		}
	}
	if len(selection.Packages) == 0 {
		report.printf("changed coverage passed: no changed Go packages relative to %s; no coverage tests ran\n", run.base)
		return nil
	}

	selectedPaths, byModule, err := partitionChangedPackages(manifest, selection.Packages, report)
	if err != nil {
		return err
	}
	if len(byModule) == 0 {
		report.printf("changed coverage passed: %d changed package(s) have no numeric coverage floor; no coverage tests ran\n", len(selectedPaths))
		return nil
	}

	measurements, profileCount, err := measureChangedModules(byModule, run, report)
	if err != nil {
		return err
	}
	if err := CompareSelected(manifest, selectedPaths, measurements); err != nil {
		return err
	}
	report.printf("changed coverage passed: %d changed package(s) checked across %d module profile(s)\n", len(selectedPaths), profileCount)
	return nil
}

// partitionChangedPackages groups registered, floor-bearing packages by
// module and rejects packages missing from the manifest.
func partitionChangedPackages(manifest Manifest, selected []WorkspacePackage, report *progressReport) ([]string, map[string][]WorkspacePackage, error) {
	entries := make(map[string]PackageEntry, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		entries[entry.ImportPath] = entry
	}
	selectedPaths := make([]string, 0, len(selected))
	missing := make([]string, 0)
	byModule := make(map[string][]WorkspacePackage)
	for _, packageInfo := range selected {
		selectedPaths = append(selectedPaths, packageInfo.ImportPath)
		entry, registered := entries[packageInfo.ImportPath]
		if !registered {
			missing = append(missing, packageInfo.ImportPath)
			continue
		}
		if entry.HasException {
			report.printf("changed coverage skipped %s: manifest exception: %s\n", packageInfo.ImportPath, entry.Exception)
			continue
		}
		byModule[packageInfo.ModuleDirectory] = append(byModule[packageInfo.ModuleDirectory], packageInfo)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, &FindingsError{Unregistered: missing}
	}
	return selectedPaths, byModule, nil
}

// measureChangedModules runs one coverage profile per module in a temporary
// directory and removes that directory before returning.
func measureChangedModules(byModule map[string][]WorkspacePackage, run coverageRun, report *progressReport) (measurements map[string]Coverage, profileCount int, returnErr error) {
	profileDirectory, err := os.MkdirTemp("", "coveragegate-changed-")
	if err != nil {
		return nil, 0, fmt.Errorf("%w: create temporary coverage directory: %w", ErrChangedPackageCoverage, err)
	}
	defer func() {
		if removeErr := os.RemoveAll(profileDirectory); removeErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("%w: remove temporary coverage directory: %w", ErrChangedPackageCoverage, removeErr)
		}
	}()

	modulePaths := make([]string, 0, len(byModule))
	for modulePath := range byModule {
		modulePaths = append(modulePaths, modulePath)
	}
	sort.Strings(modulePaths)
	profilePaths := make([]string, 0, len(modulePaths))
	for index, modulePath := range modulePaths {
		profilePath := filepath.Join(profileDirectory, fmt.Sprintf("module-%d.out", index))
		if err := runModuleCoverage(modulePath, byModule[modulePath], profilePath, run, report); err != nil {
			return nil, 0, err
		}
		profilePaths = append(profilePaths, profilePath)
	}
	measurements, err = ReadProfiles(profilePaths)
	if err != nil {
		return nil, 0, err
	}
	return measurements, len(profilePaths), nil
}

func runModuleCoverage(modulePath string, packages []WorkspacePackage, profilePath string, run coverageRun, report *progressReport) error {
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].ImportPath < packages[j].ImportPath
	})
	arguments := []string{"test", "-count=1", "-tags=nomicrophone", "-timeout", run.testTimeout, "-coverprofile", profilePath}
	for _, packageInfo := range packages {
		packageArgument, err := packageArgument(packageInfo)
		if err != nil {
			return err
		}
		arguments = append(arguments, packageArgument)
	}
	report.printf("changed coverage testing %d package(s) in %s\n", len(packages), modulePath)
	command := exec.Command(run.goBinary, arguments...)
	command.Dir = modulePath
	workspaceFile := nearestWorkspaceFile(modulePath)
	if workspaceFile == "" {
		workspaceFile = "off"
	}
	command.Env = setEnvironment(os.Environ(), "GOWORK", workspaceFile)
	command.Stdout = run.stdout
	command.Stderr = run.stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: go test for changed packages in %s failed: %w", ErrChangedPackageCoverage, modulePath, err)
	}
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
