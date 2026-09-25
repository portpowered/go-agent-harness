package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// options holds the parsed coveragegate command line.
type options struct {
	manifestPath, goBinary, gitBinary, repoDir, base, testTimeout string
	tags, selectPath                                              string
	validateRegistration, changedCoverage, affected               bool
	moduleDirs, standaloneDirs, profilePaths                      stringList
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("coveragegate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.manifestPath, "manifest", "", "path to the coverage manifest fragment directory or legacy JSON file")
	flags.StringVar(&opts.goBinary, "go", "go", "Go executable used for package discovery")
	flags.StringVar(&opts.gitBinary, "git", "git", "Git executable used for changed-package discovery")
	flags.StringVar(&opts.repoDir, "repo", ".", "repository directory used for changed-package discovery")
	flags.StringVar(&opts.base, "base", "origin/main", "comparison base used for changed-package discovery")
	flags.StringVar(&opts.testTimeout, "test-timeout", "120s", "timeout passed to changed-package go test runs")
	flags.BoolVar(&opts.validateRegistration, "validate-registration", false, "validate coverage-manifest against packages discovered in workspace modules")
	flags.BoolVar(&opts.validateRegistration, "check-registration", false, "alias for -validate-registration")
	flags.BoolVar(&opts.changedCoverage, "changed", false, "measure and enforce floors only for packages owning changed Go files")
	flags.Var(&opts.moduleDirs, "module-dir", "workspace module directory (may be repeated for registration validation)")
	flags.Var(&opts.moduleDirs, "module", "alias for -module-dir")
	flags.Var(&opts.profilePaths, "profile", "coverage profile path (may be repeated)")
	flags.BoolVar(&opts.affected, "affected", false, "print the changed-package test and floor scope (reverse dependency closure) instead of gating")
	flags.Var(&opts.standaloneDirs, "standalone-module-dir", "module directory listed with GOWORK=off for -affected (may be repeated)")
	flags.StringVar(&opts.tags, "tags", "", "build tags used to list package dependencies for -affected")
	flags.StringVar(&opts.selectPath, "select", "", "file of import paths (one per line); gate only these packages' floors against partial profiles")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	opts.profilePaths = append(opts.profilePaths, flags.Args()...)
	return opts, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	switch {
	case opts.affected:
		return runAffected(opts.gitBinary, opts.goBinary, opts.repoDir, opts.base, opts.tags, opts.moduleDirs, opts.standaloneDirs, stdout)
	case opts.changedCoverage:
		if opts.validateRegistration {
			return errors.New("changed coverage cannot be combined with registration validation")
		}
		if len(opts.profilePaths) > 0 {
			return errors.New("changed coverage does not accept explicit coverage profiles")
		}
		return runChangedCoverage(opts.manifestPath, opts.gitBinary, opts.goBinary, opts.repoDir, opts.base, opts.testTimeout, opts.moduleDirs, stdout, stderr)
	case opts.validateRegistration:
		if len(opts.profilePaths) > 0 {
			return errors.New("registration validation does not accept coverage profiles")
		}
		return runRegistration(opts.manifestPath, opts.goBinary, opts.moduleDirs, stdout)
	}
	return runGate(opts, stdout)
}

func runGate(opts options, stdout io.Writer) error {
	if opts.manifestPath == "" {
		return errors.New("coverage gate requires --manifest")
	}
	manifest, err := LoadManifest(opts.manifestPath)
	if err != nil {
		return err
	}
	if opts.selectPath != "" {
		// A changed-scope run may select packages that no test links, and
		// then produce no profile at all.
		measurements := map[string]Coverage{}
		if len(opts.profilePaths) > 0 {
			if measurements, err = ReadProfiles(opts.profilePaths); err != nil {
				return err
			}
		}
		return gateSelected(manifest, opts.selectPath, measurements, len(opts.profilePaths), stdout)
	}
	if len(opts.profilePaths) == 0 {
		return errors.New("coverage gate requires at least one coverage profile")
	}
	measurements, err := ReadProfiles(opts.profilePaths)
	if err != nil {
		return err
	}
	if err := Compare(manifest, measurements); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "coverage gate passed: %d registered packages checked across %d profiles\n", len(manifest.Packages), len(opts.profilePaths))
	return err
}

func runRegistration(manifestPath, goBinary string, moduleDirs []string, stdout io.Writer) error {
	if manifestPath == "" {
		return errors.New("registration validation requires --manifest")
	}
	if len(moduleDirs) == 0 {
		return errors.New("registration validation requires at least one --module-dir")
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	discovered, err := DiscoverWorkspacePackages(context.Background(), goBinary, moduleDirs)
	if err != nil {
		return err
	}
	if err := ValidateRegistration(manifest, discovered); err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "coverage registration passed: %d workspace packages checked across %d modules\n", len(discovered), len(moduleDirs))
	return err
}

func runAffected(gitBinary, goBinary, repoDir, base, tags string, moduleDirs, standaloneDirs []string, stdout io.Writer) error {
	if len(moduleDirs)+len(standaloneDirs) == 0 {
		return errors.New("affected scope requires at least one --module-dir")
	}
	modules := make([]AffectedModule, 0, len(moduleDirs)+len(standaloneDirs))
	for _, directory := range moduleDirs {
		modules = append(modules, AffectedModule{Directory: directory})
	}
	for _, directory := range standaloneDirs {
		modules = append(modules, AffectedModule{Directory: directory, Standalone: true})
	}
	scope, err := SelectAffectedScope(context.Background(), gitBinary, goBinary, repoDir, base, tags, modules)
	if err != nil {
		return err
	}
	return WriteAffectedScope(stdout, scope)
}

// gateSelected enforces floors only for the import paths listed in
// selectPath, so partial (changed-package) profiles do not report every
// other registered package as unmeasured.
func gateSelected(manifest Manifest, selectPath string, measurements map[string]Coverage, profileCount int, stdout io.Writer) error {
	data, err := os.ReadFile(selectPath)
	if err != nil {
		return fmt.Errorf("read --select file: %w", err)
	}
	floors := make(map[string]PackageEntry, len(manifest.Packages))
	for _, entry := range manifest.Packages {
		floors[entry.ImportPath] = entry
	}
	var selected []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// A zero floor passes whatever the measurement; a partial run may
		// not have produced a profile for its module at all.
		if entry, ok := floors[line]; ok && entry.HasMinimum && entry.MinimumCents == 0 {
			if _, measured := measurements[line]; !measured {
				continue
			}
		}
		selected = append(selected, line)
	}
	if err := CompareSelected(manifest, selected, measurements); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "coverage gate passed: %d selected packages checked across %d profiles\n", len(selected), profileCount)
	return err
}

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}
