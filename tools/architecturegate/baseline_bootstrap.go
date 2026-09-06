package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// compareBootstrapBaseline checks a newly introduced baseline against the
// exact Go source tree at merge base. The inventory evaluates both AST size
// rules and source level architecture rules; type-aware checks still require
// a baseline that already existed at the merge base.
func compareBootstrapBaseline(ctx context.Context, gitBinary, repoRoot, relative, mergeBase string, current Baseline, policy Policy) []Issue {
	if current.SourceCommit != "" && current.SourceCommit != mergeBase {
		return []Issue{{Rule: "baseline-history-source", File: filepath.ToSlash(relative), Message: fmt.Sprintf("baseline source_commit %q does not identify merge base %s", current.SourceCommit, mergeBase)}}
	}
	old, err := measureBootstrapSource(ctx, gitBinary, repoRoot, mergeBase, policy)
	if err != nil {
		return []Issue{{Rule: "baseline-history", File: filepath.ToSlash(relative), Message: err.Error()}}
	}
	return compareBootstrapEntries(old.Issues, current)
}

const (
	bootstrapDirectoryMode os.FileMode = 0o700
	bootstrapFileMode      os.FileMode = 0o600
)

func measureBootstrapSource(ctx context.Context, gitBinary, repoRoot, mergeBase string, policy Policy) (result Result, returnErr error) {
	archive, err := gitOutput(ctx, gitBinary, repoRoot, "archive", "--format=tar", mergeBase)
	if err != nil {
		return Result{}, fmt.Errorf("cannot inspect merge-base source %s for baseline bootstrap: %w", mergeBase, err)
	}
	temporary, err := os.MkdirTemp("", "architecturegate-baseline-")
	if err != nil {
		return Result{}, fmt.Errorf("create baseline bootstrap workspace: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(temporary); returnErr == nil && cleanupErr != nil {
			returnErr = fmt.Errorf("remove baseline bootstrap workspace: %w", cleanupErr)
		}
	}()
	if err := extractGitArchive(archive, temporary); err != nil {
		return Result{}, fmt.Errorf("extract merge-base source %s: %w", mergeBase, err)
	}
	modules, err := discoverSnapshotModules(temporary, policy.ModuleDirs)
	if err != nil {
		return Result{}, fmt.Errorf("inventory merge-base source %s: %w", mergeBase, err)
	}
	old, err := evaluate(ctx, modules, policy, map[string]bool{"architecture": true, "size": true}, "", "")
	if err != nil {
		return Result{}, fmt.Errorf("measure merge-base source %s: %w", mergeBase, err)
	}
	return old, nil
}

func compareBootstrapEntries(oldIssues []Issue, current Baseline) []Issue {
	oldByKey := make(map[string]Issue, len(oldIssues))
	for _, issue := range oldIssues {
		oldByKey[issue.Key()] = issue
	}
	result := make([]Issue, 0)
	for _, entry := range currentEntriesSorted(current.Entries) {
		key := baselineIssue(entry).Key()
		oldKey := key
		if source, renamed := renameSourceFor(current.Renames, key); renamed {
			oldKey = source
		}
		oldIssue, ok := oldByKey[oldKey]
		if !ok {
			result = append(result, Issue{Rule: "baseline-history-add", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Message: "new baseline exemption requires an issue present at merge base; add a reviewed migration or leave the issue live"})
			continue
		}
		if metricRule(entry.Rule) && entry.Value > oldIssue.Value {
			result = append(result, Issue{Rule: "baseline-history-increase", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Value: entry.Value, Limit: oldIssue.Value, Message: "baseline ceiling exceeds the merge-base measurement"})
			continue
		}
		if !metricRule(entry.Rule) && entry.Message != oldIssue.Message {
			result = append(result, Issue{Rule: "baseline-history-increase", Module: entry.Module, Package: entry.Package, File: entry.File, Symbol: entry.Symbol, Message: "baseline message differs from the merge-base issue"})
		}
	}
	return result
}

func extractGitArchive(data []byte, destination string) error {
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractArchiveEntry(reader, header, destination); err != nil {
			return err
		}
	}
}

func extractArchiveEntry(reader *tar.Reader, header *tar.Header, destination string) error {
	name := filepath.Clean(filepath.FromSlash(header.Name))
	if name == "." || filepath.IsAbs(name) || !pathWithin(destination, filepath.Join(destination, name)) {
		return fmt.Errorf("archive entry %q escapes extraction directory", header.Name)
	}
	target := filepath.Join(destination, name)
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, bootstrapDirectoryMode)
	case tar.TypeReg, tar.TypeRegA:
		return extractArchiveFile(reader, target)
	case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNUSparse:
		// Git may include PAX metadata records. archive/tar exposes the
		// global record as an entry even though it has no source payload.
		return nil
	default:
		return fmt.Errorf("unsupported archive entry type %d for %q", header.Typeflag, header.Name)
	}
}

func extractArchiveFile(reader *tar.Reader, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), bootstrapDirectoryMode); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, bootstrapFileMode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// discoverSnapshotModules builds the size-only inventory without invoking Go
// tooling from the historical checkout. The merge base may predate this
// standalone module or have different replacements, but its physical source
// remains sufficient to identify exact legacy metric issues.
func discoverSnapshotModules(root string, dirs []string) ([]*Module, error) {
	modules := make([]*Module, 0, len(dirs))
	for _, rawDir := range dirs {
		module, err := discoverSnapshotModule(root, rawDir)
		if err != nil {
			return nil, err
		}
		if module != nil {
			modules = append(modules, module)
		}
	}
	return modules, nil
}

func discoverSnapshotModule(root, rawDir string) (*Module, error) {
	dir := filepath.Join(root, filepath.FromSlash(rawDir))
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("module %q is not a directory", rawDir)
	}
	modulePath, err := readModulePath(dir)
	if err != nil {
		return nil, err
	}
	module := &Module{Dir: canonicalPath(dir), Path: modulePath}
	packagesByDir := make(map[string]*Package)
	if err := walkSnapshotSources(module, packagesByDir); err != nil {
		return nil, fmt.Errorf("inventory historical module %q: %w", rawDir, err)
	}
	markSnapshotTypeLoadability(packagesByDir)
	return finalizeModule(module, packagesByDir)
}

func walkSnapshotSources(module *Module, packagesByDir map[string]*Package) error {
	return filepath.WalkDir(module.Dir, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return skipInventoryDirectory(entry.Name())
		}
		if filepath.Ext(name) != ".go" {
			return nil
		}
		return inventorySource(module, module.Path, name, packagesByDir, false)
	})
}
