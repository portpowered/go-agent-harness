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

const defaultToolDirectory = ".cache/go-tools"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("analyzergate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tool := flags.String("tool", "", "analyzer name (golangci-lint or staticcheck)")
	expectedVersion := flags.String("expected-version", "", "pinned analyzer version")
	candidate := flags.String("candidate", "", "PATH name or path of the candidate analyzer")
	goBinary := flags.String("go", "go", "Go executable used to install the pinned analyzer")
	installPackage := flags.String("install-package", "", "Go package to install; the expected version is appended when needed")
	toolDirectory := flags.String("tool-dir", defaultToolDirectory, "root directory for deterministic installed analyzer binaries")
	workingDirectory := flags.String("working-dir", ".", "directory used for relative paths and analyzer version probes")
	var versionArgs stringList
	flags.Var(&versionArgs, "version-arg", "argument passed to the analyzer version probe (may be repeated)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("analyzer resolver does not accept positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	cfg, err := newConfig(configInput{
		tool:             *tool,
		expectedVersion:  *expectedVersion,
		candidate:        *candidate,
		goBinary:         *goBinary,
		installPackage:   *installPackage,
		toolDirectory:    *toolDirectory,
		workingDirectory: *workingDirectory,
		versionArgs:      []string(versionArgs),
	})
	if err != nil {
		return err
	}
	resolved, err := resolve(context.Background(), cfg, stderr)
	if err != nil {
		return err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	_, err = fmt.Fprintln(stdout, resolved)
	return err
}

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type configInput struct {
	tool             string
	expectedVersion  string
	candidate        string
	goBinary         string
	installPackage   string
	toolDirectory    string
	workingDirectory string
	versionArgs      []string
}

func newConfig(input configInput) (config, error) {
	spec, ok := analyzerSpecs[input.tool]
	if !ok {
		return config{}, fmt.Errorf("unsupported analyzer %q; expected one of golangci-lint or staticcheck", input.tool)
	}

	expectedVersion, err := normalizeVersion(input.expectedVersion)
	if err != nil {
		return config{}, fmt.Errorf("invalid pinned %s version: %w", input.tool, err)
	}
	pinnedVersion := strings.TrimSpace(input.expectedVersion)
	goBinary := strings.TrimSpace(input.goBinary)
	if goBinary == "" {
		return config{}, errors.New("analyzer resolver requires a Go executable")
	}
	toolDirectory := strings.TrimSpace(input.toolDirectory)
	if toolDirectory == "" {
		return config{}, errors.New("analyzer resolver requires a tool directory")
	}
	workingDirectory := strings.TrimSpace(input.workingDirectory)
	if workingDirectory == "" {
		return config{}, errors.New("analyzer resolver requires a working directory")
	}
	workingDirectory, err = absolutePath(workingDirectory)
	if err != nil {
		return config{}, fmt.Errorf("resolve analyzer working directory: %w", err)
	}
	toolDirectory, err = absolutePath(toolDirectory)
	if err != nil {
		return config{}, fmt.Errorf("resolve analyzer tool directory: %w", err)
	}

	candidate := strings.TrimSpace(input.candidate)
	if candidate == "" {
		candidate = spec.binaryName
	}
	installPackage := strings.TrimSpace(input.installPackage)
	if installPackage == "" {
		installPackage = spec.installPackage
	}
	if strings.ContainsAny(installPackage, "\r\n") {
		return config{}, errors.New("analyzer resolver install package must be a single line")
	}
	if strings.Contains(installPackage, "@") {
		return config{}, fmt.Errorf("analyzer resolver install package %q must not include a version; expected version is appended", installPackage)
	}

	if len(input.versionArgs) == 0 {
		input.versionArgs = append([]string(nil), spec.versionArgs...)
	}
	return config{
		tool:             input.tool,
		expectedVersion:  expectedVersion,
		pinnedVersion:    pinnedVersion,
		candidate:        candidate,
		goBinary:         goBinary,
		installPackage:   installPackage,
		binaryName:       spec.binaryName,
		versionArgs:      input.versionArgs,
		toolDirectory:    toolDirectory,
		workingDirectory: workingDirectory,
	}, nil
}
