// Command architecturegate enforces the repository's service boundaries and
// physical complexity budgets. It is intentionally a small, standalone
// module so the gate does not become part of a product module's dependency
// graph.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	options, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	repoRoot, err := filepath.Abs(options.repo)
	if err != nil {
		return fmt.Errorf("resolve repository %q: %w", options.repo, err)
	}
	repoRoot = filepath.Clean(repoRoot)
	manifest, err := loadPolicy(options.manifestPath, repoRoot)
	if err != nil {
		return err
	}
	moduleDirs := options.moduleDirs(manifest)
	if len(moduleDirs) == 0 {
		return errors.New("architecture gate requires at least one -module-dir or manifest module_dirs entry")
	}
	modules, err := discoverModulesForTarget(context.Background(), options.goBinary, repoRoot, moduleDirs, options.patterns(manifest), options.goos, options.goarch)
	if err != nil {
		return err
	}
	if len(modules) == 0 {
		return errors.New("architecture gate selected no modules")
	}
	result, err := evaluate(context.Background(), modules, manifest, options.checkSet, options.goos, options.goarch)
	if err != nil {
		return err
	}
	if err := applyBaseline(&result, modules, options, manifest, repoRoot); err != nil {
		return err
	}
	result.Sort()
	return reportResult(stdout, options.format, result)
}

type runOptions struct {
	repo, manifestPath, baselinePath, baselineBase string
	checkSet                                       map[string]bool
	format, goBinary, gitBinary, goos, goarch      string
	modules, packagePatterns                       stringList
}

func parseOptions(args []string, stderr io.Writer) (runOptions, error) {
	flags := flag.NewFlagSet("architecturegate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository root used to resolve module and manifest paths")
	manifestPath := flags.String("manifest", "", "architecture policy manifest (JSON)")
	baselinePath := flags.String("baseline", "", "exact deletion-only architecture/size baseline (JSON)")
	baselineBase := flags.String("baseline-base", "", "git ref used to reject baseline additions or increases")
	check := flags.String("check", "all", "checks to run: architecture, size, all")
	format := flags.String("format", "text", "report format: text or json")
	goBinary := flags.String("go", "go", "Go executable used for package discovery")
	gitBinary := flags.String("git", "git", "Git executable used for baseline history checks")
	var moduleDirs stringList
	flags.Var(&moduleDirs, "module-dir", "workspace module directory (may be repeated)")
	flags.Var(&moduleDirs, "module", "alias for -module-dir")
	var patterns stringList
	flags.Var(&patterns, "pattern", "package pattern passed to go list (may be repeated)")
	flags.Var(&patterns, "scope", "alias for -pattern")
	goos := flags.String("goos", "", "GOOS used for type loading (empty keeps the host value)")
	goarch := flags.String("goarch", "", "GOARCH used for type loading (empty keeps the host value)")
	if err := flags.Parse(args); err != nil {
		return runOptions{}, err
	}
	if len(flags.Args()) > 0 {
		return runOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	if *format != "text" && *format != "json" {
		return runOptions{}, fmt.Errorf("unsupported -format %q; expected text or json", *format)
	}
	checkSet, err := parseChecks(*check)
	if err != nil {
		return runOptions{}, err
	}
	return runOptions{repo: *repo, manifestPath: *manifestPath, baselinePath: *baselinePath, baselineBase: *baselineBase, checkSet: checkSet, format: *format, goBinary: *goBinary, gitBinary: *gitBinary, goos: *goos, goarch: *goarch, modules: moduleDirs, packagePatterns: patterns}, nil
}

func (o runOptions) moduleDirs(policy Policy) stringList {
	if len(o.modules) > 0 {
		return o.modules
	}
	return stringList(append([]string(nil), policy.ModuleDirs...))
}

func (o runOptions) patterns(policy Policy) stringList {
	if len(o.packagePatterns) > 0 {
		return o.packagePatterns
	}
	if len(policy.Patterns) > 0 {
		return stringList(append([]string(nil), policy.Patterns...))
	}
	return stringList{"./..."}
}

func applyBaseline(result *Result, modules []*Module, options runOptions, manifest Policy, repoRoot string) error {
	if !options.checkSet["size"] && !options.checkSet["architecture"] {
		return nil
	}
	baselinePath := options.baselinePath
	if baselinePath == "" {
		baselinePath = manifest.Baseline
	}
	if baselinePath == "" {
		if strings.TrimSpace(options.baselineBase) != "" {
			return errors.New("-baseline-base requires a reviewed -baseline or manifest baseline path")
		}
		return nil
	}
	baseline, err := loadBaseline(baselinePath, repoRoot)
	if err != nil {
		return err
	}
	baseline = baselineForChecks(baseline, options.checkSet)
	baseline = baselineForScope(baseline, modules, options.patterns(manifest))
	result.Issues = compareBaseline(result.Issues, baseline)
	if strings.TrimSpace(options.baselineBase) == "" {
		return nil
	}
	baselineAbs, err := resolveRepoPath(baselinePath, repoRoot)
	if err != nil {
		return err
	}
	result.Issues = append(result.Issues, compareBaselineHistory(context.Background(), options.gitBinary, repoRoot, baselineAbs, options.baselineBase, baseline, manifest)...)
	return nil
}

// baselineForModules keeps a scoped invocation from reporting every unrelated
// repository entry as stale. Full policy runs retain every module; focused
// checks compare only the module directories they actually inventory.
func baselineForModules(baseline Baseline, modules []*Module) Baseline {
	if len(modules) == 0 {
		return baseline
	}
	selected := make(map[string]struct{}, len(modules))
	for _, module := range modules {
		if module != nil {
			selected[module.Path] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return baseline
	}
	entries := make([]BaselineEntry, 0, len(baseline.Entries))
	kept := make(map[string]struct{}, len(baseline.Entries))
	for _, entry := range baseline.Entries {
		if _, ok := selected[entry.Module]; !ok {
			continue
		}
		entries = append(entries, entry)
		kept[baselineIssue(entry).Key()] = struct{}{}
	}
	renamed := make([]BaselineRename, 0, len(baseline.Renames))
	for _, rename := range baseline.Renames {
		if _, ok := kept[rename.To]; ok {
			renamed = append(renamed, rename)
		}
	}
	return Baseline{Version: baseline.Version, SourceCommit: baseline.SourceCommit, Entries: entries, Renames: renamed}
}

// baselineForScope applies the package patterns used by go list after the
// module filter. It deliberately matches package prefixes from the requested
// pattern instead of only currently discovered packages: a removed package
// inside a focused prefix must remain visible as a stale baseline entry, while
// an unrelated package in the same module must stay out of the report.
func baselineForScope(baseline Baseline, modules []*Module, patterns []string) Baseline {
	baseline = baselineForModules(baseline, modules)
	if len(patterns) == 0 || baselineHasWholeModulePattern(patterns) {
		return baseline
	}
	selectedModules := make(map[string]*Module, len(modules))
	for _, module := range modules {
		if module != nil {
			selectedModules[module.Path] = module
		}
	}
	entries := make([]BaselineEntry, 0, len(baseline.Entries))
	kept := make(map[string]struct{}, len(baseline.Entries))
	for _, entry := range baseline.Entries {
		module := selectedModules[entry.Module]
		if module == nil || entry.Package == "" || packageMatchesPatterns(module.Path, entry.Package, patterns) {
			entries = append(entries, entry)
			kept[baselineIssue(entry).Key()] = struct{}{}
		}
	}
	renamed := make([]BaselineRename, 0, len(baseline.Renames))
	for _, rename := range baseline.Renames {
		if _, ok := kept[rename.To]; ok {
			renamed = append(renamed, rename)
		}
	}
	return Baseline{Version: baseline.Version, SourceCommit: baseline.SourceCommit, Entries: entries, Renames: renamed}
}

func baselineHasWholeModulePattern(patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(filepath.ToSlash(pattern))
		if pattern == "..." || pattern == "./..." || pattern == "**" {
			return true
		}
	}
	return false
}

func packageMatchesPatterns(modulePath, packagePath string, patterns []string) bool {
	for _, rawPattern := range patterns {
		pattern := strings.TrimSpace(filepath.ToSlash(rawPattern))
		if pattern == "" || pattern == "..." || pattern == "./..." {
			return true
		}
		if strings.HasPrefix(pattern, "./") {
			pattern = modulePath + "/" + strings.TrimPrefix(pattern, "./")
		} else if !strings.HasPrefix(pattern, modulePath) {
			pattern = modulePath + "/" + strings.TrimPrefix(pattern, "/")
		}
		if strings.HasSuffix(pattern, "/...") {
			prefix := strings.TrimSuffix(pattern, "/...")
			if packagePath == prefix || strings.HasPrefix(packagePath, prefix+"/") {
				return true
			}
			continue
		}
		if packagePath == pattern {
			return true
		}
	}
	return false
}

func reportResult(stdout io.Writer, format string, result Result) error {
	if format == "json" {
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else {
		if err := writeText(stdout, result); err != nil {
			return err
		}
	}
	if len(result.Issues) > 0 {
		return fmt.Errorf("architecture gate failed with %d issue(s)", len(result.Issues))
	}
	return nil
}

func parseChecks(value string) (map[string]bool, error) {
	set := make(map[string]bool)
	for _, raw := range strings.Split(value, ",") {
		name := strings.TrimSpace(strings.ToLower(raw))
		if name == "all" {
			set["architecture"], set["size"] = true, true
			continue
		}
		if name != "architecture" && name != "size" {
			return nil, fmt.Errorf("unsupported check %q; expected architecture, size, or all", name)
		}
		set[name] = true
	}
	if len(set) == 0 {
		return nil, errors.New("-check must select at least one check")
	}
	return set, nil
}

func writeText(w io.Writer, result Result) error {
	if len(result.Issues) == 0 {
		_, err := fmt.Fprintf(w, "architecture gate passed: %d package(s), %d file(s), %d function(s) checked\n", result.Packages, result.Files, result.Functions)
		return err
	}
	if _, err := fmt.Fprintf(w, "architecture gate found %d issue(s) across %d package(s):\n", len(result.Issues), result.Packages); err != nil {
		return err
	}
	for _, issue := range result.Issues {
		if _, err := fmt.Fprintf(w, "- %s\n", issue.String()); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(w io.Writer, result Result) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("list flag value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

// Result is intentionally serializable: CI can archive the complete report
// while the human output remains compact.
type Result struct {
	Packages  int     `json:"packages"`
	Files     int     `json:"files"`
	Functions int     `json:"functions"`
	Issues    []Issue `json:"issues,omitempty"`
}

func (r *Result) Sort() {
	sort.Slice(r.Issues, func(i, j int) bool { return r.Issues[i].Less(r.Issues[j]) })
}
