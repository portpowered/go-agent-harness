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
	validateRegistration := false
	flags.BoolVar(&validateRegistration, "validate-registration", false, "validate coverage-manifest.json against packages discovered in workspace modules")
	flags.BoolVar(&validateRegistration, "check-registration", false, "alias for -validate-registration")
	var moduleDirs stringList
	flags.Var(&moduleDirs, "module-dir", "workspace module directory (may be repeated for registration validation)")
	flags.Var(&moduleDirs, "module", "alias for -module-dir")
	var profilePaths stringList
	flags.Var(&profilePaths, "profile", "coverage profile path (may be repeated)")
	if err := flags.Parse(args); err != nil {
		return err
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

	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read coverage manifest %q: %w", manifestPath, err)
	}
	manifest, err := ParseManifest(manifestData)
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
