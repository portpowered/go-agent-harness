package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("coveragegate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "path to the coverage manifest fragment directory or legacy JSON file")
	goBinary := flags.String("go", "go", "Go executable used for package discovery")
	gitBinary := flags.String("git", "git", "Git executable used for changed-package discovery")
	repoDir := flags.String("repo", ".", "repository directory used for changed-package discovery")
	base := flags.String("base", "origin/main", "comparison base used for changed-package discovery")
	testTimeout := flags.String("test-timeout", "120s", "timeout passed to changed-package go test runs")
	validateRegistration := false
	flags.BoolVar(&validateRegistration, "validate-registration", false, "validate coverage-manifest against packages discovered in workspace modules")
	flags.BoolVar(&validateRegistration, "check-registration", false, "alias for -validate-registration")
	changedCoverage := false
	flags.BoolVar(&changedCoverage, "changed", false, "measure and enforce floors only for packages owning changed Go files")
	var moduleDirs stringList
	flags.Var(&moduleDirs, "module-dir", "workspace module directory (may be repeated for registration validation)")
	flags.Var(&moduleDirs, "module", "alias for -module-dir")
	var profilePaths stringList
	flags.Var(&profilePaths, "profile", "coverage profile path (may be repeated)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if changedCoverage {
		if validateRegistration {
			return errors.New("changed coverage cannot be combined with registration validation")
		}
		if len(profilePaths) > 0 || len(flags.Args()) > 0 {
			return errors.New("changed coverage does not accept explicit coverage profiles")
		}
		return runChangedCoverage(*manifestPath, *gitBinary, *goBinary, *repoDir, *base, *testTimeout, moduleDirs, stdout, stderr)
	}
	if validateRegistration {
		if len(profilePaths) > 0 {
			return errors.New("registration validation does not accept coverage profiles")
		}
		return runRegistration(*manifestPath, *goBinary, moduleDirs, stdout)
	}
	if *manifestPath == "" {
		return errors.New("coverage gate requires --manifest")
	}
	profilePaths = append(profilePaths, flags.Args()...)
	if len(profilePaths) == 0 {
		return errors.New("coverage gate requires at least one coverage profile")
	}

	manifest, err := LoadManifest(*manifestPath)
	if err != nil {
		return err
	}
	measurements, err := ReadProfiles(profilePaths)
	if err != nil {
		return err
	}
	if err := Compare(manifest, measurements); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "coverage gate passed: %d registered packages checked across %d profiles\n", len(manifest.Packages), len(profilePaths))
	return nil
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

	_, _ = fmt.Fprintf(stdout, "coverage registration passed: %d workspace packages checked across %d modules\n", len(discovered), len(moduleDirs))
	return nil
}

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}
