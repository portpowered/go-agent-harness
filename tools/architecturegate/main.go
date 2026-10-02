// Command architecturegate enforces the repository's service boundaries:
// service shape, contract purity, public-surface leaks, composition authority
// and the import and source rules golangci-lint cannot express. Size,
// complexity, package-global and init limits belong to golangci-lint (see
// docs/architecture/lint-policy.md). It is intentionally a small, standalone
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
	result, err := evaluate(context.Background(), modules, manifest, options.goos, options.goarch)
	if err != nil {
		return err
	}
	result.Sort()
	return reportResult(stdout, options.format, result)
}

type runOptions struct {
	repo, manifestPath             string
	format, goBinary, goos, goarch string
	modules, packagePatterns       stringList
}

func parseOptions(args []string, stderr io.Writer) (runOptions, error) {
	flags := flag.NewFlagSet("architecturegate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository root used to resolve module and manifest paths")
	manifestPath := flags.String("manifest", "", "architecture policy manifest (JSON)")
	format := flags.String("format", "text", "report format: text or json")
	goBinary := flags.String("go", "go", "Go executable used for package discovery")
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
	return runOptions{repo: *repo, manifestPath: *manifestPath, format: *format, goBinary: *goBinary, goos: *goos, goarch: *goarch, modules: moduleDirs, packagePatterns: patterns}, nil
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

func writeText(w io.Writer, result Result) error {
	if len(result.Issues) == 0 {
		_, err := fmt.Fprintf(w, "architecture gate passed: %d package(s), %d file(s) checked\n", result.Packages, result.Files)
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
	Packages int     `json:"packages"`
	Files    int     `json:"files"`
	Issues   []Issue `json:"issues,omitempty"`
}

func (r *Result) Sort() {
	sort.Slice(r.Issues, func(i, j int) bool { return r.Issues[i].Less(r.Issues[j]) })
}
