package main

import (
	"bytes"
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

var (
	ErrChangedPackageSelection = errors.New("changed-package selection failed")
	ErrChangedPackageCoverage  = errors.New("changed-package coverage failed")
)

// WorkspacePackage identifies a package in the current workspace and the
// module from which it can be tested.
type WorkspacePackage struct {
	ImportPath      string
	Directory       string
	ModuleDirectory string
}

// ChangedPackageSelection is the deterministic result of inspecting all
// committed and worktree changes. GoFiles and UnownedGoFiles are repository
// relative paths; an unowned file is a deleted package or a Go file outside
// the currently discoverable workspace packages.
type ChangedPackageSelection struct {
	Packages       []WorkspacePackage
	GoFiles        []string
	UnownedGoFiles []string
}

type fileChange struct {
	Path    string
	OldPath string
}

// DiscoverWorkspacePackageDetails uses go list to resolve each current
// workspace package to an on-disk directory. It deliberately discovers only
// packages in the supplied modules, not dependencies or helper modules.
func DiscoverWorkspacePackageDetails(ctx context.Context, goBinary string, moduleDirs []string) ([]WorkspacePackage, error) {
	if len(moduleDirs) == 0 {
		return nil, fmt.Errorf("%w: no workspace module directories were provided", ErrChangedPackageSelection)
	}
	if strings.TrimSpace(goBinary) == "" {
		goBinary = "go"
	}

	packages := make(map[string]WorkspacePackage)
	seenModules := make(map[string]struct{}, len(moduleDirs))
	for _, rawModuleDir := range moduleDirs {
		moduleDir := strings.TrimSpace(rawModuleDir)
		if moduleDir == "" {
			return nil, fmt.Errorf("%w: workspace module directory is empty", ErrChangedPackageSelection)
		}
		absoluteModuleDir, err := filepath.Abs(moduleDir)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve workspace module directory %q: %v", ErrChangedPackageSelection, moduleDir, err)
		}
		absoluteModuleDir = filepath.Clean(absoluteModuleDir)
		absoluteModuleDir = canonicalPath(absoluteModuleDir)
		if _, duplicate := seenModules[absoluteModuleDir]; duplicate {
			return nil, fmt.Errorf("%w: workspace module directory %q was provided more than once", ErrChangedPackageSelection, moduleDir)
		}
		seenModules[absoluteModuleDir] = struct{}{}
		info, err := os.Stat(absoluteModuleDir)
		if err != nil {
			return nil, fmt.Errorf("%w: inspect workspace module directory %q: %v", ErrChangedPackageSelection, moduleDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: workspace module path %q is not a directory", ErrChangedPackageSelection, moduleDir)
		}

		var stdout, stderr strings.Builder
		command := exec.CommandContext(ctx, goBinary, "list", "-json", "./...")
		command.Dir = absoluteModuleDir
		workspaceFile := nearestWorkspaceFile(absoluteModuleDir)
		if workspaceFile == "" {
			workspaceFile = "off"
		}
		command.Env = setEnvironment(os.Environ(), "GOWORK", workspaceFile)
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			return nil, externalCommandError(ErrChangedPackageSelection, "go list", err, stdout.String(), stderr.String(), moduleDir)
		}

		decoder := json.NewDecoder(strings.NewReader(stdout.String()))
		modulePackages := 0
		for {
			var listed struct {
				ImportPath string
				Dir        string
			}
			err := decoder.Decode(&listed)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%w: decode go list output in %q: %v", ErrChangedPackageSelection, moduleDir, err)
			}
			if strings.TrimSpace(listed.ImportPath) == "" || strings.TrimSpace(listed.Dir) == "" {
				return nil, fmt.Errorf("%w: go list returned a package without import path or directory in %q", ErrChangedPackageSelection, moduleDir)
			}
			listedDir := listed.Dir
			if !filepath.IsAbs(listedDir) {
				listedDir = filepath.Join(absoluteModuleDir, listedDir)
			}
			packageDir, err := filepath.Abs(listedDir)
			if err != nil {
				return nil, fmt.Errorf("%w: resolve directory for package %q: %v", ErrChangedPackageSelection, listed.ImportPath, err)
			}
			packageDir = filepath.Clean(packageDir)
			packageDir = canonicalPath(packageDir)
			if !pathWithin(absoluteModuleDir, packageDir) {
				return nil, fmt.Errorf("%w: package %q directory %q is outside module %q", ErrChangedPackageSelection, listed.ImportPath, packageDir, moduleDir)
			}
			modulePackages++
			if previous, duplicate := packages[listed.ImportPath]; duplicate {
				return nil, fmt.Errorf("%w: package %q was listed by both %q and %q", ErrChangedPackageSelection, listed.ImportPath, previous.ModuleDirectory, absoluteModuleDir)
			}
			packages[listed.ImportPath] = WorkspacePackage{
				ImportPath:      listed.ImportPath,
				Directory:       packageDir,
				ModuleDirectory: absoluteModuleDir,
			}
		}
		if modulePackages == 0 {
			return nil, fmt.Errorf("%w: go list in %q returned no packages", ErrChangedPackageSelection, moduleDir)
		}
	}

	result := make([]WorkspacePackage, 0, len(packages))
	for _, packageInfo := range packages {
		result = append(result, packageInfo)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ImportPath < result[j].ImportPath
	})
	return result, nil
}

// SelectChangedPackages resolves Go files changed since base, staged,
// unstaged, or newly present and not ignored, to current workspace package
// directories. It never falls back to selecting every package: no changed
// Go files and deleted packages with no current directory both produce an
// empty Packages result with an explanatory UnownedGoFiles entry where useful.
func SelectChangedPackages(ctx context.Context, gitBinary, goBinary, repoDir, base string, moduleDirs []string) (ChangedPackageSelection, error) {
	repoRoot, err := filepath.Abs(repoDir)
	if err != nil {
		return ChangedPackageSelection{}, fmt.Errorf("%w: resolve repository directory %q: %v", ErrChangedPackageSelection, repoDir, err)
	}
	info, err := os.Stat(repoRoot)
	if err != nil {
		return ChangedPackageSelection{}, fmt.Errorf("%w: inspect repository directory %q: %v", ErrChangedPackageSelection, repoDir, err)
	}
	if !info.IsDir() {
		return ChangedPackageSelection{}, fmt.Errorf("%w: repository path %q is not a directory", ErrChangedPackageSelection, repoDir)
	}
	repoRoot = canonicalPath(repoRoot)

	changes, err := collectGitChanges(ctx, gitBinary, repoRoot, base)
	if err != nil {
		return ChangedPackageSelection{}, err
	}
	changedPaths := make(map[string]struct{})
	for _, change := range changes {
		if change.Path != "" {
			changedPaths[change.Path] = struct{}{}
		}
		if change.OldPath != "" {
			changedPaths[change.OldPath] = struct{}{}
		}
	}
	goFiles := make([]string, 0)
	for changedPath := range changedPaths {
		if isGoFile(changedPath) {
			goFiles = append(goFiles, changedPath)
		}
	}
	sort.Strings(goFiles)
	selection := ChangedPackageSelection{GoFiles: goFiles}
	if len(goFiles) == 0 {
		return selection, nil
	}

	workspacePackages, err := DiscoverWorkspacePackageDetails(ctx, goBinary, moduleDirs)
	if err != nil {
		return ChangedPackageSelection{}, err
	}
	byDirectory := make(map[string]WorkspacePackage, len(workspacePackages))
	for _, packageInfo := range workspacePackages {
		if previous, duplicate := byDirectory[packageInfo.Directory]; duplicate && previous.ImportPath != packageInfo.ImportPath {
			return ChangedPackageSelection{}, fmt.Errorf("%w: packages %q and %q share directory %q", ErrChangedPackageSelection, previous.ImportPath, packageInfo.ImportPath, packageInfo.Directory)
		}
		byDirectory[packageInfo.Directory] = packageInfo
	}
	selected := make(map[string]WorkspacePackage)
	for _, changedPath := range goFiles {
		absolutePath, err := repositoryPath(repoRoot, changedPath)
		if err != nil {
			selection.UnownedGoFiles = append(selection.UnownedGoFiles, changedPath)
			continue
		}
		packageInfo, ok := byDirectory[filepath.Dir(absolutePath)]
		if !ok {
			selection.UnownedGoFiles = append(selection.UnownedGoFiles, changedPath)
			continue
		}
		selected[packageInfo.ImportPath] = packageInfo
	}
	for _, packageInfo := range selected {
		selection.Packages = append(selection.Packages, packageInfo)
	}
	sort.Slice(selection.Packages, func(i, j int) bool {
		return selection.Packages[i].ImportPath < selection.Packages[j].ImportPath
	})
	sort.Strings(selection.UnownedGoFiles)
	return selection, nil
}

func collectGitChanges(ctx context.Context, gitBinary, repoRoot, base string) ([]fileChange, error) {
	if strings.TrimSpace(gitBinary) == "" {
		gitBinary = "git"
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, fmt.Errorf("%w: comparison base is empty; set --base or COVERAGE_BASE", ErrChangedPackageSelection)
	}
	mergeBaseOutput, err := runGit(ctx, gitBinary, repoRoot, "merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOutput))
	if strings.ContainsAny(mergeBase, "\r\n") || mergeBase == "" {
		return nil, fmt.Errorf("%w: git merge-base returned an invalid commit for base %q: %q", ErrChangedPackageSelection, base, mergeBase)
	}

	var changes []fileChange
	for _, arguments := range [][]string{
		{"diff", "--name-status", "--find-renames", "-z", mergeBase, "HEAD"},
		{"diff", "--cached", "--name-status", "--find-renames", "-z"},
		{"diff", "--name-status", "--find-renames", "-z"},
	} {
		output, err := runGit(ctx, gitBinary, repoRoot, arguments...)
		if err != nil {
			return nil, err
		}
		parsed, err := parseGitNameStatus(output)
		if err != nil {
			return nil, fmt.Errorf("%w: parse git diff output: %v", ErrChangedPackageSelection, err)
		}
		changes = append(changes, parsed...)
	}
	untracked, err := runGit(ctx, gitBinary, repoRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range splitNUL(untracked) {
		if path != "" {
			changes = append(changes, fileChange{Path: path})
		}
	}
	return changes, nil
}

func parseGitNameStatus(data []byte) ([]fileChange, error) {
	fields := splitNUL(data)
	changes := make([]fileChange, 0, len(fields)/2)
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" {
			continue
		}
		if index >= len(fields) || fields[index] == "" {
			return nil, fmt.Errorf("status %q has no path", status)
		}
		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(fields) || fields[index+1] == "" {
				return nil, fmt.Errorf("rename/copy status %q does not contain old and new paths", status)
			}
			changes = append(changes, fileChange{OldPath: fields[index], Path: fields[index+1]})
			index += 2
			continue
		}
		changes = append(changes, fileChange{Path: fields[index]})
		index++
	}
	return changes, nil
}

func splitNUL(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, string(part))
	}
	return result
}

func runGit(ctx context.Context, gitBinary, repoRoot string, arguments ...string) ([]byte, error) {
	commandArguments := append([]string{"-C", repoRoot}, arguments...)
	command := exec.CommandContext(ctx, gitBinary, commandArguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, externalCommandError(ErrChangedPackageSelection, "git "+strings.Join(arguments, " "), err, stdout.String(), stderr.String(), repoRoot)
	}
	return stdout.Bytes(), nil
}

func externalCommandError(kind error, commandName string, commandErr error, stdout, stderr, scope string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	if detail != "" {
		return fmt.Errorf("%w: %s in %q failed: %v: %s", kind, commandName, scope, commandErr, detail)
	}
	return fmt.Errorf("%w: %s in %q failed: %v", kind, commandName, scope, commandErr)
}

func repositoryPath(repoRoot, repositoryRelativePath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(repositoryRelativePath))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the repository", repositoryRelativePath)
	}
	return filepath.Join(repoRoot, clean), nil
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) && !filepath.IsAbs(relative)
}

func canonicalPath(path string) string {
	if evaluated, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(evaluated)
	}
	return filepath.Clean(path)
}

func isGoFile(path string) bool {
	return strings.HasSuffix(path, ".go")
}
