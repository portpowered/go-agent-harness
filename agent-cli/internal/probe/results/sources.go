// Package results reads recorded probe result artifacts for the friction
// report and the fleet gate, and renders the friction report summary.
package results

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

const (
	// StdinSource is the artifact path that reads standard input.
	StdinSource = "-"
	// artifactFilePermission is the mode of a written report or verdict.
	artifactFilePermission = 0o644
)

// source is one opened artifact.
type source struct {
	name   string
	reader io.Reader
}

// openSources opens every path in order, mapping StdinSource to stdin.
// prepare validates or normalizes one path before it is opened and
// openError maps an open failure. On failure every opened file is closed;
// on success the returned function closes them.
func openSources(paths []string, stdin io.Reader, prepare func(string) (string, error), openError func(string, error) error) ([]source, func(), error) {
	sources := make([]source, 0, len(paths))
	files := make([]*os.File, 0, len(paths))
	closeAll := func() {
		for _, file := range files {
			_ = file.Close() //nolint:errcheck // Read-only artifact handles; a close failure cannot lose data.
		}
	}
	for _, raw := range paths {
		path, err := prepare(raw)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		if path == StdinSource {
			sources = append(sources, source{name: StdinSource, reader: stdin})
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			closeAll()
			return nil, func() {}, openError(path, err)
		}
		files = append(files, file)
		sources = append(sources, source{name: path, reader: file})
	}
	return sources, closeAll, nil
}

// OpenFrictionInputs opens the friction report inputs. Paths are trimmed and
// must not be empty; an unreadable input is a typed FrictionReportError.
func OpenFrictionInputs(paths []string, stdin io.Reader) ([]probe.FrictionReportInput, func(), error) {
	prepare := func(raw string) (string, error) {
		path := strings.TrimSpace(raw)
		if path == "" {
			return "", fmt.Errorf("--out input path must not be empty")
		}
		return path, nil
	}
	openError := func(path string, err error) error {
		return &probe.FrictionReportError{Source: path, Err: fmt.Errorf("open input: %w", err)}
	}
	sources, closeAll, err := openSources(paths, stdin, prepare, openError)
	if err != nil {
		return nil, closeAll, err
	}
	inputs := make([]probe.FrictionReportInput, 0, len(sources))
	for _, opened := range sources {
		inputs = append(inputs, probe.FrictionReportInput{Name: opened.name, Reader: opened.reader})
	}
	return inputs, closeAll, nil
}

// OpenGateArtifacts opens the fleet gate's run result artifacts.
func OpenGateArtifacts(paths []string, stdin io.Reader) ([]probe.FleetArtifact, func(), error) {
	prepare := func(raw string) (string, error) { return raw, nil }
	openError := func(_ string, err error) error { return fmt.Errorf("read result artifact: %w", err) }
	sources, closeAll, err := openSources(paths, stdin, prepare, openError)
	if err != nil {
		return nil, closeAll, err
	}
	artifacts := make([]probe.FleetArtifact, 0, len(sources))
	for _, opened := range sources {
		artifacts = append(artifacts, probe.FleetArtifact{Name: opened.name, Reader: opened.reader})
	}
	return artifacts, closeAll, nil
}

// WriteFile writes one report or verdict artifact.
func WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, artifactFilePermission)
}
