package sessionfixturevalidator

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const sessionCaptureSuffix = ".session.json"

// ErrValidationFailed indicates that fixture hygiene violations were reported.
var ErrValidationFailed = errors.New("session fixture validation failed")

// Result summarizes one command validation run.
type Result struct {
	FilesScanned int
	Errors       []gatewaytesting.SessionFixtureValidationError
}

// Run validates session fixture files from command arguments and writes user-facing output.
func Run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("session-fixture-validator", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var usageErr error
	flags.Usage = func() {
		usageErr = writeUsage(flags.Output(), flags.Name())
	}

	emitManifestPath := flags.String("emit-manifest", "", "write a sorted fixture manifest for the scanned roots to this `FILE` instead of validating")

	if err := flags.Parse(args); err != nil {
		return errors.Join(err, usageErr)
	}

	paths := flags.Args()
	if *emitManifestPath != "" {
		return runEmitManifest(*emitManifestPath, paths, stdout)
	}
	if len(paths) == 0 {
		flags.Usage()
		return errors.Join(errors.New("at least one file or directory is required"), usageErr)
	}

	result, err := ValidatePaths(paths)
	if err != nil {
		return err
	}

	if len(result.Errors) > 0 {
		for _, validationErr := range result.Errors {
			if _, err := fmt.Fprintln(stderr, validationErr.Error()); err != nil {
				return errors.Join(ErrValidationFailed, err)
			}
		}
		return ErrValidationFailed
	}

	_, err = fmt.Fprintf(stdout, "validated %d session fixture file(s): ok\n", result.FilesScanned)
	return err
}

const usageFormat = `Usage: %s [-emit-manifest FILE] [files-or-directories...]

Validates committed .session.json captures for fixture hygiene.
Checks require session.fixture_provenance, reject unsafe raw audio or credential-like fields and values,
and ensure provider wire events use payload_type "websocket_message" instead of generic "stream_message".
With -emit-manifest FILE, writes a generated manifest of the scanned fixture set
`

func writeUsage(w io.Writer, name string) error {
	_, err := fmt.Fprintf(w, usageFormat, name)
	return err
}

// ValidatePaths scans files and directories, then applies the shared session fixture validator.
func ValidatePaths(paths []string) (Result, error) {
	files, err := collectSessionFixtureFiles(paths)
	if err != nil {
		return Result{}, err
	}

	result := Result{FilesScanned: len(files)}
	for _, file := range files {
		result.Errors = append(result.Errors, validateSessionFixtureFile(file)...)
	}
	return result, nil
}

// runEmitManifest scans paths and writes the generated fixture manifest to
// outputPath instead of validating.
func runEmitManifest(outputPath string, paths []string, stdout io.Writer) error {
	roots, err := validateCommittedFixtureRootArguments(paths)
	if err != nil {
		return err
	}
	manifest, err := buildCommittedFixtureManifest(roots)
	if err != nil {
		return err
	}
	data, err := manifest.render()
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("write fixture manifest %s: %w", outputPath, err)
	}
	_, err = fmt.Fprintf(stdout, "wrote fixture manifest %s: %d session fixture file(s)\n", outputPath, manifest.Count)
	return err
}

func collectSessionFixtureFiles(paths []string) ([]string, error) {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		if err := filepath.WalkDir(path, func(childPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionCaptureSuffix) {
				return nil
			}
			files = append(files, childPath)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	sort.Strings(files)
	return files, nil
}

// currentDirectoryOrEmpty resolves the process working directory. An
// unavailable directory leaves repository-root discovery to its caller-file
// fallback, so the lookup failure is represented as an empty anchor.
func currentDirectoryOrEmpty() string {
	directory, err := filepath.Abs(".")
	if err != nil {
		return ""
	}
	return directory
}
