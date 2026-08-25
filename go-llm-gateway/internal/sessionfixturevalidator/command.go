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
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), "Usage: %s [-emit-manifest FILE] [files-or-directories...]\n\n", flags.Name())
		_, _ = fmt.Fprintln(flags.Output(), "Validates committed .session.json captures for fixture hygiene.")
		_, _ = fmt.Fprintln(flags.Output(), "Checks require session.fixture_provenance, reject unsafe raw audio or credential-like fields and values,")
		_, _ = fmt.Fprintln(flags.Output(), "and ensure provider wire events use payload_type \"websocket_message\" instead of generic \"stream_message\".")
		_, _ = fmt.Fprintln(flags.Output(), "With -emit-manifest FILE, writes a generated manifest of the scanned fixture set")
	}

	emitManifestPath := flags.String("emit-manifest", "", "write a sorted fixture manifest for the scanned roots to this `FILE` instead of validating")

	if err := flags.Parse(args); err != nil {
		return err
	}

	paths := flags.Args()
	if *emitManifestPath != "" {
		return runEmitManifest(*emitManifestPath, paths, stdout)
	}
	if len(paths) == 0 {
		flags.Usage()
		return errors.New("at least one file or directory is required")
	}

	result, err := ValidatePaths(paths)
	if err != nil {
		return err
	}

	if len(result.Errors) > 0 {
		for _, validationErr := range result.Errors {
			_, _ = fmt.Fprintln(stderr, validationErr.Error())
		}
		return ErrValidationFailed
	}

	_, _ = fmt.Fprintf(stdout, "validated %d session fixture file(s): ok\n", result.FilesScanned)
	return nil
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
	if len(paths) == 0 {
		return errors.New("-emit-manifest requires at least one file or directory")
	}
	manifest, err := buildFixtureManifest(paths)
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
	_, _ = fmt.Fprintf(stdout, "wrote fixture manifest %s: %d session fixture file(s)\n", outputPath, manifest.Count)
	return nil
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
