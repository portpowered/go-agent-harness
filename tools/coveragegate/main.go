package main

import (
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
	manifestPath := flags.String("manifest", "", "path to the coverage manifest JSON file")
	var profilePaths stringList
	flags.Var(&profilePaths, "profile", "coverage profile path (may be repeated)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" {
		return errors.New("coverage gate requires --manifest")
	}
	profilePaths = append(profilePaths, flags.Args()...)
	if len(profilePaths) == 0 {
		return errors.New("coverage gate requires at least one coverage profile")
	}

	manifestData, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read coverage manifest %q: %w", *manifestPath, err)
	}
	manifest, err := ParseManifest(manifestData)
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

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}
